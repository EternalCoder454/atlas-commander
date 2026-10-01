plugins {
	id("com.android.application")
	id("org.jetbrains.kotlin.plugin.compose")
}

// The version comes from the repository's VERSION file, the one place the
// project records it, so the app cannot drift from the desktop.
val atlasVersion: String by lazy {
	rootProject.file("../VERSION").readText().trim().removeSuffix("-beta")
}

// One ever-increasing integer: 0.5.10 becomes 510.
val atlasVersionCode: Int by lazy {
	val p = atlasVersion.split(".").map { it.toIntOrNull() ?: 0 }
	p.getOrElse(0) { 0 } * 10000 + p.getOrElse(1) { 0 } * 100 + p.getOrElse(2) { 0 }
}

android {
	namespace = "com.atlas.commander"
	compileSdk = 35

	defaultConfig {
		applicationId = "com.atlas.commander"
		minSdk = 29
		targetSdk = 35
		versionCode = atlasVersionCode
		versionName = atlasVersion
	}

	// Debug signing with the stable ~/.android/debug.keystore is what gets
	// sideloaded for now; release reuses it until a real key exists.
	buildTypes {
		release {
			isMinifyEnabled = false
			signingConfig = signingConfigs.getByName("debug")
		}
	}

	buildFeatures {
		compose = true
	}

	testOptions {
		unitTests.isReturnDefaultValues = true
	}

	packaging {
		resources.excludes += setOf("META-INF/*.kotlin_module", "META-INF/LICENSE*")
	}

	compileOptions {
		sourceCompatibility = JavaVersion.VERSION_17
		targetCompatibility = JavaVersion.VERSION_17
	}
}

dependencies {
	implementation("androidx.core:core-ktx:1.15.0")
	implementation("androidx.activity:activity-compose:1.9.3")
	implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")

	val compose = platform("androidx.compose:compose-bom:2024.11.00")
	implementation(compose)
	implementation("androidx.compose.ui:ui")
	implementation("androidx.compose.ui:ui-graphics")
	implementation("androidx.compose.material3:material3")
	implementation("androidx.compose.material:material-icons-core")

	// Google's code scanner: it scans in a Play services screen, so the app
	// needs no camera permission.
	implementation("com.google.android.gms:play-services-code-scanner:16.1.0")

	testImplementation("junit:junit:4.13.2")
	// The Android SDK's org.json is a stub on the JVM, so tests bring the real one.
	testImplementation("org.json:json:20240303")
}
