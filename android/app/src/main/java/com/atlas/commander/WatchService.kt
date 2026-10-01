package com.atlas.commander

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.Uri
import android.os.Build
import android.os.IBinder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

private const val WATCH_ID = 1
private const val ACTION_DECIDE = "com.atlas.commander.DECIDE"

/** Seconds to wait after 0, 1, 2+ consecutive failed polls. */
private val BACKOFF_MS = longArrayOf(5000, 15000, 60000)

/**
 * Request codes for the Allow and Deny buttons: one fresh number per
 * (approval, action), so two approvals can never share a PendingIntent the way
 * hash codes could. Each intent also carries a unique data URI, which keeps
 * them apart even if this map is lost when the process restarts.
 */
private object RequestCodes {
	private val codes = HashMap<String, Int>()

	@Synchronized
	fun of(id: String, allow: Boolean): Int = codes.getOrPut("$id/$allow") { codes.size + 1 }
}

private fun openApp(context: Context): PendingIntent = PendingIntent.getActivity(
	context, 0, Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
	PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
)

private fun decide(context: Context, a: Approval, allow: Boolean): PendingIntent = PendingIntent.getBroadcast(
	context, RequestCodes.of(a.id, allow),
	Intent(context, ApprovalActionReceiver::class.java).setAction(ACTION_DECIDE)
		.setData(Uri.parse("atlas-approval://decide/${Uri.encode(a.id)}/${if (allow) "allow" else "deny"}"))
		.putExtra("id", a.id).putExtra("allow", allow)
		.putExtra("agent", a.agentName).putExtra("tool", a.tool).putExtra("summary", a.summary),
	PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
)

private fun action(label: String, intent: PendingIntent): Notification.Action {
	val b = Notification.Action.Builder(null, label, intent)
	// Allow and Deny must not work from a locked phone.
	if (Build.VERSION.SDK_INT >= 31) b.setAuthenticationRequired(true)
	return b.build()
}

/**
 * The approval notification. It is private on the lock screen (only a generic
 * line shows) and [note] replaces the summary when a decision failed.
 */
private fun approvalNotification(context: Context, a: Approval, note: String? = null): Notification {
	val lockScreen = Notification.Builder(context, CHANNEL_APPROVALS)
		.setSmallIcon(android.R.drawable.stat_sys_warning)
		.setContentTitle("An agent is waiting for approval")
		.build()
	val text = note ?: a.summary
	return Notification.Builder(context, CHANNEL_APPROVALS)
		.setSmallIcon(android.R.drawable.stat_sys_warning)
		.setContentTitle("${a.agentName} wants to use ${a.tool}")
		.setContentText(text)
		.setStyle(Notification.BigTextStyle().bigText(text))
		.setContentIntent(openApp(context))
		.setAutoCancel(true)
		.setOnlyAlertOnce(true)
		.setVisibility(Notification.VISIBILITY_PRIVATE)
		.setPublicVersion(lockScreen)
		.addAction(action("Allow", decide(context, a, true)))
		.addAction(action("Deny", decide(context, a, false)))
		.build()
}


/**
 * Polls /state every 5 s while the app is closed and raises a notification for
 * each new approval, with Allow and Deny buttons that call the API directly.
 * It stops itself when the phone is unpaired or the setting is switched off.
 */
class WatchService : Service() {
	private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
	private var job: Job? = null
	private val notified = HashSet<String>()

	override fun onBind(intent: Intent?): IBinder? = null

	override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
		val store = Store(this)
		val pairing = store.pairing()
		// Android requires startForeground after startForegroundService, even
		// when the service is about to stop, or it kills the app.
		val n = Notification.Builder(this, CHANNEL_WATCH)
			.setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
			.setContentTitle("Watching ${pairing?.name?.ifBlank { null } ?: "your PC"}")
			.setOngoing(true)
			.setContentIntent(openApp(this))
			.build()
		startForeground(WATCH_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
		if (pairing == null || !store.notify) {
			stopSelf()
			return START_NOT_STICKY
		}
		if (job?.isActive != true) {
			// Approvals already showing from before this start must not alert again.
			val nm = getSystemService(NotificationManager::class.java)
			for (a in nm.activeNotifications) {
				if (a.notification.channelId == CHANNEL_APPROVALS && a.tag != null) notified.add(a.tag)
			}
			job = scope.launch { loop(store) }
		}
		return START_STICKY
	}

	// Android 15 limits dataSync services to six hours a day and then calls
	// this; stopping cleanly is required, and opening the app starts it again.
	override fun onTimeout(startId: Int, fgsType: Int) {
		stopSelf()
	}

	override fun onDestroy() {
		scope.cancel()
		super.onDestroy()
	}

	private suspend fun loop(store: Store) {
		var client: ApiClient? = null
		var forPairing: Pairing? = null
		var failures = 0
		while (scope.isActive) {
			val p = store.pairing()
			if (p == null || !store.notify) {
				stopSelf()
				return
			}
			if (client == null || forPairing != p) {
				client = ApiClient(p, onHostWorked = { store.rememberHost(it) })
				forPairing = p
			}
			try {
				val s = client.state()
				post(s.approvals)
				failures = 0
			} catch (e: ApiException) {
				if (e.unauthorized) {
					stopSelf()
					return
				}
				failures++
			} catch (_: Exception) {
				// Unreachable: stay quiet and retry, less often the longer it lasts.
				failures++
			}
			delay(BACKOFF_MS[minOf(failures, BACKOFF_MS.size - 1)])
		}
	}

	private fun post(approvals: List<Approval>) {
		val nm = getSystemService(NotificationManager::class.java)
		val current = approvals.map { it.id }.toSet()
		// An approval decided elsewhere (the PC, another phone) loses its notification.
		for (id in notified.filter { it !in current }) nm.cancel(id, 0)
		notified.retainAll(current)
		for (a in approvals) {
			if (!notified.add(a.id)) continue
			val n = approvalNotification(this, a)
			nm.notify(a.id, 0, n)
		}
	}
}

/** Handles the Allow and Deny buttons on an approval notification. */
class ApprovalActionReceiver : BroadcastReceiver() {
	override fun onReceive(context: Context, intent: Intent) {
		val id = intent.getStringExtra("id") ?: return
		val allow = intent.getBooleanExtra("allow", false)
		val store = Store(context)
		val pairing = store.pairing() ?: return
		val approval = Approval(
			id, "", intent.getStringExtra("agent").orEmpty(), intent.getStringExtra("tool").orEmpty(),
			intent.getStringExtra("summary").orEmpty(), "", "",
		)
		// A broadcast has about ten seconds, so the PC gets a short timeout and
		// only the first few hosts; finish() runs on every path.
		val quick = pairing.copy(hosts = pairing.hostsToTry().take(3))
		val pending = goAsync()
		Thread {
			val nm = context.getSystemService(NotificationManager::class.java)
			try {
				ApiClient(quick, timeoutMs = 2000, onHostWorked = { store.rememberHost(it) }).decide(id, allow)
				nm.cancel(id, 0)
			} catch (e: ApiException) {
				// Already decided elsewhere (404 or 409) means the card is stale too.
				if (e.status == 404 || e.status == 409) {
					nm.cancel(id, 0)
				} else {
					retry(context, nm, approval, pairing)
				}
			} catch (_: Exception) {
				retry(context, nm, approval, pairing)
			} finally {
				pending.finish()
			}
		}.start()
	}

	/** The button press dismissed the notification, so a failed decision puts it back. */
	private fun retry(context: Context, nm: NotificationManager, a: Approval, pairing: Pairing) {
		val pc = pairing.name.ifBlank { "your PC" }
		nm.notify(a.id, 0, approvalNotification(context, a, "Couldn't reach $pc. Try again."))
	}
}
