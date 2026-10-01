package com.atlas.commander

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

const val CHANNEL_WATCH = "watch"
const val CHANNEL_APPROVALS = "approvals"

/** The message shown for an unreachable PC (the wording is part of the spec). */
fun unreachableText(name: String) =
	"Can't reach $name. Is Commander running with phone access on, and are you on the same network?"

/** The message for a failed call: a changed certificate gets its own advice. */
fun failText(name: String, e: Exception) =
	if (e is UnreachableException && e.pinMismatch) {
		"$name answered with a different certificate than when you paired. If phone access was reset on the PC, pair again."
	} else {
		unreachableText(name)
	}

class AtlasApp : Application() {
	lateinit var model: AppModel
		private set

	override fun onCreate() {
		super.onCreate()
		model = AppModel(this)
		val nm = getSystemService(NotificationManager::class.java)
		nm.createNotificationChannel(
			NotificationChannel(CHANNEL_WATCH, "Watching your PC", NotificationManager.IMPORTANCE_LOW),
		)
		nm.createNotificationChannel(
			NotificationChannel(CHANNEL_APPROVALS, "Approvals", NotificationManager.IMPORTANCE_HIGH),
		)
	}
}

/**
 * What the screens show. The polling loop and the actions write here from
 * coroutines; Compose reads it. Network calls always run on Dispatchers.IO.
 */
class AppModel(private val context: Context) {
	val store = Store(context)
	private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

	var pairing by mutableStateOf(store.pairing())
		private set
	var state by mutableStateOf<FleetState?>(null)
		private set

	/** Why the last refresh failed, or null when it worked. */
	var error by mutableStateOf<String?>(null)
		private set

	/** Set after a 401: the pairing is stale and the pair screen shows this text. */
	var repairMessage by mutableStateOf<String?>(null)
		private set

	/** One-shot message for the snackbar. */
	var toast by mutableStateOf<String?>(null)
	var pairBusy by mutableStateOf(false)
		private set
	var pairError by mutableStateOf<String?>(null)
	var notifyEnabled by mutableStateOf(store.notify)
		private set

	/** A pairing link that arrived from outside the app and awaits the user's tap. */
	var pendingLink by mutableStateOf<Pairing?>(null)
		private set

	private var client: ApiClient? = null

	val needsPairing get() = pairing == null || repairMessage != null

	fun pcName() = pairing?.name?.ifBlank { null } ?: state?.name ?: "your PC"

	fun api(): ApiClient? {
		val p = pairing ?: return null
		return client ?: ApiClient(p, onHostWorked = { host -> store.rememberHost(host) }).also { client = it }
	}

	/** One refresh of /state. Safe to call from the main thread. */
	suspend fun refresh() {
		val c = api() ?: return
		try {
			val s = withContext(Dispatchers.IO) { c.state() }
			// The pairing may have changed while the call was in flight; an
			// answer from the old client must not land on the new state.
			if (client !== c) return
			state = s
			error = null
		} catch (e: ApiException) {
			if (client === c) handle(e)
		} catch (e: Exception) {
			if (client === c) error = failText(pcName(), e)
		}
	}

	private fun handle(e: ApiException) {
		if (e.unauthorized) {
			repairMessage = e.message
			stopWatching()
		} else {
			error = e.message
		}
	}

	/** Runs a call, then refreshes. API errors go to the snackbar as the PC worded them. */
	fun act(call: ApiClient.() -> Unit) {
		val c = api() ?: return
		scope.launch {
			try {
				withContext(Dispatchers.IO) { c.call() }
			} catch (e: ApiException) {
				if (client === c) {
					if (e.unauthorized) handle(e) else toast = e.message
				}
			} catch (e: Exception) {
				if (client === c) toast = failText(pcName(), e)
			}
			refresh()
		}
	}

	fun killAll() = act { killAll() }

	/**
	 * A link opened from outside the app (a browser, a message) is only parsed
	 * here; pairing waits for the user to confirm, because it replaces any
	 * existing pairing.
	 */
	fun offerLink(text: String) {
		val p = PairingLink.parse(text)
		if (p == null) {
			pairError = "That isn't a Commander pairing link."
			return
		}
		pendingLink = p
	}

	fun confirmLink() {
		val p = pendingLink ?: return
		pendingLink = null
		pair(p)
	}

	fun dismissLink() {
		pendingLink = null
	}

	/** Pairs from pasted or scanned text, where the user is acting directly. */
	fun pairFromText(text: String) {
		val p = PairingLink.parse(text)
		if (p == null) {
			pairError = "That isn't a Commander pairing link."
			return
		}
		pair(p)
	}

	/** Validates by pinging, then saves. */
	private fun pair(p: Pairing) {
		pairBusy = true
		pairError = null
		scope.launch {
			try {
				var worked = ""
				val ping = withContext(Dispatchers.IO) { ApiClient(p, onHostWorked = { worked = it }).ping() }
				store.save(p.copy(name = ping.name.ifBlank { p.name }, lastHost = worked))
				pairing = store.pairing()
				client = null
				state = null
				error = null
				repairMessage = null
				store.notify = true
				notifyEnabled = true
				refresh()
			} catch (e: ApiException) {
				pairError = e.message
			} catch (e: Exception) {
				pairError = failText(p.name, e)
			} finally {
				pairBusy = false
			}
		}
	}

	fun unpair() {
		stopWatching() // also removes any approval notifications
		store.clear()
		pairing = null
		client = null
		state = null
		error = null
		repairMessage = null
		pairError = null
		notifyEnabled = true
	}

	fun setNotify(on: Boolean) {
		store.notify = on
		notifyEnabled = on
		if (on) {
			store.notifDeniedFor = ""
			startWatching()
		} else {
			stopWatching()
		}
	}

	fun startWatching() {
		if (pairing == null || !store.notify) return
		try {
			context.startForegroundService(Intent(context, WatchService::class.java))
		} catch (_: Exception) {
			// Android can refuse a foreground start from the background; the
			// next time the app is opened it tries again.
		}
	}

	/** Stops the service and removes approval notifications, whose buttons would no longer work. */
	fun stopWatching() {
		context.stopService(Intent(context, WatchService::class.java))
		val nm = context.getSystemService(NotificationManager::class.java)
		for (n in nm.activeNotifications) {
			if (n.notification.channelId == CHANNEL_APPROVALS) nm.cancel(n.tag, n.id)
		}
	}
}
