plugins {
	id("com.android.application") version "9.4.1" apply false
	// AGP 9 compiles Kotlin itself, so there is no Kotlin Android plugin. The
	// Compose compiler plugin stays, and its version is the Kotlin version.
	id("org.jetbrains.kotlin.plugin.compose") version "2.4.20" apply false
}
