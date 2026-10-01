package com.atlas.commander

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

/** Status colours: the same tones the desktop uses (internal/theme, fleet.Status.Tone). */
class StatusColors(val ok: Color, val warn: Color, val error: Color, val idle: Color) {
	fun forStatus(status: String): Color = when (status) {
		"running", "waiting", "starting" -> ok
		"approval", "held", "capped" -> warn
		"error" -> error
		else -> idle
	}
}

val LocalStatusColors = staticCompositionLocalOf { StatusColors(Color.Gray, Color.Gray, Color.Gray, Color.Gray) }

// From internal/theme/builtin/dark.json and light.json, the libadwaita palettes.
private val darkScheme = darkColorScheme(
	primary = Color(0xFF3584E4), onPrimary = Color.White,
	secondary = Color(0xFF81D1FF), onSecondary = Color(0xFF00283D),
	background = Color(0xFF222226), onBackground = Color.White,
	surface = Color(0xFF2E2E32), onSurface = Color.White,
	surfaceVariant = Color(0xFF353539), onSurfaceVariant = Color(0xFFC8C8CC),
	surfaceContainer = Color(0xFF2E2E32), surfaceContainerHigh = Color(0xFF36363A),
	error = Color(0xFFFF938C), onError = Color(0xFF3A0000),
	outline = Color(0xFF646467), outlineVariant = Color(0xFF414144),
)

private val lightScheme = lightColorScheme(
	primary = Color(0xFF1C71D8), onPrimary = Color.White,
	secondary = Color(0xFF0461BE), onSecondary = Color.White,
	background = Color(0xFFFAFAFB), onBackground = Color(0xFF323237),
	surface = Color(0xFFFFFFFF), onSurface = Color(0xFF323237),
	surfaceVariant = Color(0xFFEBEBED), onSurfaceVariant = Color(0xFF5E5E63),
	surfaceContainer = Color(0xFFFFFFFF), surfaceContainerHigh = Color(0xFFFFFFFF),
	error = Color(0xFFC30000), onError = Color.White,
	outline = Color(0xFF8A8A8E), outlineVariant = Color(0xFFDFDFE0),
)

private val darkStatus = StatusColors(Color(0xFF78E9AB), Color(0xFFFFC252), Color(0xFFFF938C), Color(0xFF646467))
private val lightStatus = StatusColors(Color(0xFF007C3D), Color(0xFF905400), Color(0xFFC30000), Color(0xFF8A8A8E))

@Composable
fun AtlasTheme(content: @Composable () -> Unit) {
	val dark = isSystemInDarkTheme()
	androidx.compose.runtime.CompositionLocalProvider(LocalStatusColors provides if (dark) darkStatus else lightStatus) {
		MaterialTheme(colorScheme = if (dark) darkScheme else lightScheme, content = content)
	}
}
