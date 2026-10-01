package com.atlas.commander

import android.content.Context
import java.net.URLDecoder
import java.security.MessageDigest

/**
 * Everything the phone knows about one paired PC. [hosts] is in the order the
 * PC listed them; [lastHost] is the one that answered last and is tried first.
 */
data class Pairing(
	val name: String,
	val hosts: List<String>,
	val token: String,
	val fingerprint: String,
	val lastHost: String = "",
) {
	/** Hosts in the order to try them: the remembered one first. */
	fun hostsToTry(): List<String> =
		if (lastHost.isNotEmpty() && lastHost in hosts) listOf(lastHost) + hosts.filter { it != lastHost } else hosts
}

/** Parsing of the `atlascommander://pair?...` link (docs/phone-api.md). */
object PairingLink {
	private val hexPin = Regex("^[0-9a-f]{64}$")

	/** Returns the pairing, or null when the text is not a usable link. */
	fun parse(text: String): Pairing? {
		val s = text.trim()
		val prefix = "atlascommander://pair"
		if (!s.startsWith(prefix, ignoreCase = true)) return null
		val rest = s.substring(prefix.length)
		if (!rest.startsWith("?")) return null
		val q = HashMap<String, String>()
		for (part in rest.substring(1).substringBefore('#').split('&')) {
			if (part.isEmpty()) continue
			val k = part.substringBefore('=')
			val v = part.substringAfter('=', "")
			try {
				q[URLDecoder.decode(k, "UTF-8")] = URLDecoder.decode(v, "UTF-8")
			} catch (_: IllegalArgumentException) {
				return null
			}
		}
		if (q["v"] != "1") return null
		val token = q["t"].orEmpty()
		// The fingerprint is compared as lowercase hex, so it is normalised here.
		val fp = q["f"].orEmpty().lowercase()
		val hosts = q["h"].orEmpty().split(',').map { it.trim() }.filter { it.isNotEmpty() }
		if (token.isEmpty() || !hexPin.matches(fp) || hosts.isEmpty()) return null
		val name = q["n"].orEmpty().ifBlank { hosts.first() }
		return Pairing(name, hosts, token, fp)
	}
}

/** The certificate pin: lowercase hex SHA-256 of the leaf certificate's DER bytes. */
object Pin {
	fun fingerprint(der: ByteArray): String =
		MessageDigest.getInstance("SHA-256").digest(der).joinToString("") { "%02x".format(it) }

	fun matches(der: ByteArray, pin: String): Boolean =
		MessageDigest.isEqual(fingerprint(der).toByteArray(), pin.lowercase().toByteArray())
}

/** Pairing and settings in app-private preferences (backups are off in the manifest). */
class Store(context: Context) {
	private val p = context.applicationContext.getSharedPreferences("atlas", Context.MODE_PRIVATE)

	fun pairing(): Pairing? {
		val token = p.getString("token", null) ?: return null
		val fp = p.getString("fingerprint", null) ?: return null
		val hosts = p.getString("hosts", "").orEmpty().split(',').filter { it.isNotEmpty() }
		if (hosts.isEmpty()) return null
		return Pairing(p.getString("name", "").orEmpty(), hosts, token, fp, p.getString("last_host", "").orEmpty())
	}

	fun save(x: Pairing) {
		p.edit()
			.putString("name", x.name).putString("hosts", x.hosts.joinToString(","))
			.putString("token", x.token).putString("fingerprint", x.fingerprint)
			.putString("last_host", x.lastHost).apply()
	}

	fun rememberHost(host: String) {
		p.edit().putString("last_host", host).apply()
	}

	fun clear() {
		p.edit().clear().apply()
	}

	/** On by default once paired. */
	var notify: Boolean
		get() = p.getBoolean("notify", true)
		set(v) {
			p.edit().putBoolean("notify", v).apply()
		}
}
