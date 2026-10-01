package com.atlas.commander

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
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
		if (pairing == null || !store.notify) {
			stopSelf()
			return START_NOT_STICKY
		}
		val n = Notification.Builder(this, CHANNEL_WATCH)
			.setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
			.setContentTitle("Watching ${pairing.name.ifBlank { "your PC" }}")
			.setOngoing(true)
			.setContentIntent(openApp())
			.build()
		startForeground(WATCH_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
		if (job?.isActive != true) job = scope.launch { loop(store) }
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
		while (scope.isActive) {
			val p = store.pairing()
			if (p == null || !store.notify) {
				stopSelf()
				return
			}
			if (client == null || forPairing != p) {
				client = ApiClient(p) { store.rememberHost(it) }
				forPairing = p
			}
			try {
				val s = client.state()
				post(s.approvals)
			} catch (e: ApiException) {
				if (e.unauthorized) {
					stopSelf()
					return
				}
			} catch (_: Exception) {
				// Unreachable: stay quiet and try again in five seconds.
			}
			delay(5000)
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
			val n = Notification.Builder(this, CHANNEL_APPROVALS)
				.setSmallIcon(android.R.drawable.stat_sys_warning)
				.setContentTitle("${a.agentName} wants to use ${a.tool}")
				.setContentText(a.summary)
				.setStyle(Notification.BigTextStyle().bigText(a.summary))
				.setContentIntent(openApp())
				.setAutoCancel(true)
				.addAction(Notification.Action.Builder(null, "Allow", decide(a.id, true)).build())
				.addAction(Notification.Action.Builder(null, "Deny", decide(a.id, false)).build())
				.build()
			nm.notify(a.id, 0, n)
		}
	}

	private fun openApp(): PendingIntent = PendingIntent.getActivity(
		this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
		PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
	)

	private fun decide(id: String, allow: Boolean): PendingIntent = PendingIntent.getBroadcast(
		this, id.hashCode() * 2 + if (allow) 1 else 0,
		Intent(this, ApprovalActionReceiver::class.java).setAction(ACTION_DECIDE)
			.putExtra("id", id).putExtra("allow", allow),
		PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
	)
}

/** Handles the Allow and Deny buttons on an approval notification. */
class ApprovalActionReceiver : BroadcastReceiver() {
	override fun onReceive(context: Context, intent: Intent) {
		val id = intent.getStringExtra("id") ?: return
		val allow = intent.getBooleanExtra("allow", false)
		val pairing = Store(context).pairing() ?: return
		val pending = goAsync()
		Thread {
			val nm = context.getSystemService(NotificationManager::class.java)
			try {
				ApiClient(pairing).decide(id, allow)
				nm.cancel(id, 0)
			} catch (e: ApiException) {
				// Already decided elsewhere (404 or 409) means the card is stale too.
				if (e.status == 404 || e.status == 409) nm.cancel(id, 0)
			} catch (_: Exception) {
				// Unreachable: the notification stays so the user can try again.
			} finally {
				pending.finish()
			}
		}.start()
	}
}
