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
			if (granted) model.startWatching()
		}

	override fun onCreate(savedInstanceState: Bundle?) {
		super.onCreate(savedInstanceState)
		enableEdgeToEdge()
		handleLink(intent)
		setContent {
			AtlasTheme {
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
				ensureNotifications()
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

	private fun handleLink(intent: Intent?) {
		val data = intent?.data ?: return
		if (data.scheme == "atlascommander") model.pairFromText(data.toString())
	}

	/** Starts the watch service when allowed, asking for the notification permission first. */
	private fun ensureNotifications() {
		if (model.pairing == null || !model.notifyEnabled) return
		if (Build.VERSION.SDK_INT >= 33 &&
			ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
		) {
			notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
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
