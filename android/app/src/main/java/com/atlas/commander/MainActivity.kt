package com.atlas.commander

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.snapshotFlow
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
	private val model get() = (application as AtlasApp).model

	private val notifPermission =
		registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
			if (granted) {
				model.startWatching()
			} else {
				model.pairing?.let { model.store.notifDeniedFor = it.token }
			}
		}

	override fun onCreate(savedInstanceState: Bundle?) {
		super.onCreate(savedInstanceState)
		enableEdgeToEdge()
		// Only a fresh launch handles the link. A recreation (rotation, theme
		// change, process restore) hands the same intent back and must not
		// offer the pairing again.
		if (savedInstanceState == null) handleLink(intent)
		setContent {
			AtlasTheme {
				model.pendingLink?.let { link -> LinkDialog(link) }
				Root(
					model = model,
					scan = ::scan,
					setNotify = { on ->
						model.setNotify(on)
						if (on) ensureNotifications()
					},
					version = version(),
				)
			}
		}
		// The poll runs only while the app is visible; the watch service covers
		// the rest.
		lifecycleScope.launch {
			repeatOnLifecycle(Lifecycle.State.STARTED) {
				// Also fires when the pairing changes, so a first or new pairing
				// starts watching without waiting for the app to be reopened.
				launch { snapshotFlow { model.pairing?.token }.collect { ensureNotifications() } }
				while (true) {
					if (!model.needsPairing) model.refresh()
					delay(2000)
				}
			}
		}
	}

	override fun onNewIntent(intent: Intent) {
		super.onNewIntent(intent)
		handleLink(intent)
	}

	/** Offers the link for confirmation, then drops it from the intent so it is handled once. */
	private fun handleLink(intent: Intent?) {
		val data = intent?.data ?: return
		setIntent(Intent())
		if (data.scheme == "atlascommander") model.offerLink(data.toString())
	}

	@Composable
	private fun LinkDialog(link: Pairing) {
		val current = model.pairing
		AlertDialog(
			onDismissRequest = { model.dismissLink() },
			title = { Text("Pair with ${link.name} at ${link.hosts.first()}?") },
			text = { if (current != null) Text("This replaces ${current.name.ifBlank { "your current PC" }}.") },
			confirmButton = { TextButton(onClick = { model.confirmLink() }) { Text("Pair") } },
			dismissButton = { TextButton(onClick = { model.dismissLink() }) { Text("Cancel") } },
		)
	}

	/** Starts the watch service when allowed, asking for the notification permission first. */
	private fun ensureNotifications() {
		val pairing = model.pairing
		if (pairing == null || !model.notifyEnabled) return
		if (Build.VERSION.SDK_INT >= 33 &&
			ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
		) {
			// Asked once per pairing; the Settings switch asks again on request.
			if (model.store.notifDeniedFor != pairing.token) notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
			return
		}
		model.startWatching()
	}

	private fun scan() {
		val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).build()
		GmsBarcodeScanning.getClient(this, options).startScan()
			.addOnSuccessListener { code -> code.rawValue?.let { model.pairFromText(it) } }
			.addOnFailureListener { model.pairError = "Couldn't scan. Paste the link instead." }
	}

	private fun version(): String =
		try {
			packageManager.getPackageInfo(packageName, 0).versionName.orEmpty()
		} catch (_: Exception) {
			""
		}
}
