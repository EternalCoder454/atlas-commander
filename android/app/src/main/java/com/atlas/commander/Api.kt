package com.atlas.commander

import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocketFactory
import javax.net.ssl.X509TrustManager

/** The PC answered, with an error. [status] is the HTTP status, [message] the API's own text. */
class ApiException(val status: Int, message: String) : Exception(message) {
	val unauthorized get() = status == 401
}

/**
 * No host answered at all. [pinMismatch] is set when a host did answer but
 * with a certificate other than the paired one, which needs different advice.
 */
class UnreachableException(val pinMismatch: Boolean = false) : IOException("unreachable")

/** The paired certificate did not match; told apart from other TLS failures. */
class PinMismatchException : CertificateException("certificate does not match the pairing")

/**
 * Trusts exactly one certificate: the one whose SHA-256 is [pin]. The system
 * trust store is not consulted, and nothing is trusted without the check.
 */
class PinTrustManager(private val pin: String) : X509TrustManager {
	override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
		throw CertificateException("client certificates are not used")
	}

	override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
		val leaf = chain?.firstOrNull() ?: throw CertificateException("no certificate")
		if (!Pin.matches(leaf.encoded, pin)) throw PinMismatchException()
	}

	override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/** Client for the phone API (docs/phone-api.md). Blocking: call it off the main thread. */
class ApiClient(private var pairing: Pairing, private val onHostWorked: (String) -> Unit = {}) {
	private val socketFactory: SSLSocketFactory = SSLContext.getInstance("TLS").also {
		it.init(null, arrayOf(PinTrustManager(pairing.fingerprint)), null)
	}.socketFactory

	// Host names are not checked: the pin is the check, and the PC is reached by
	// bare IP addresses that no certificate names.
	private val verifier = HostnameVerifier { _, _ -> true }

	private fun call(method: String, path: String, body: JSONObject? = null): JSONObject {
		var lastError: IOException? = null
		var pinMismatch = false
		for (host in pairing.hostsToTry()) {
			val c = try {
				open(host, method, path)
			} catch (e: IOException) {
				lastError = e
				continue
			}
			try {
				if (body != null) {
					c.doOutput = true
					c.setRequestProperty("Content-Type", "application/json")
					c.outputStream.use { it.write(body.toString().toByteArray()) }
				}
				val status = c.responseCode
				val stream = if (status in 200..299) c.inputStream else c.errorStream
				val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
				if (host != pairing.lastHost) {
					pairing = pairing.copy(lastHost = host)
					onHostWorked(host)
				}
				val json = try {
					JSONObject(text)
				} catch (_: Exception) {
					JSONObject()
				}
				if (status !in 200..299) {
					throw ApiException(status, json.s("error").ifBlank { "The PC answered with error $status." })
				}
				return json
			} catch (e: ApiException) {
				throw e
			} catch (e: IOException) {
				lastError = e
				if (generateSequence<Throwable>(e) { it.cause }.any { it is PinMismatchException }) pinMismatch = true
			} finally {
				c.disconnect()
			}
		}
		throw UnreachableException(pinMismatch).also { if (lastError != null) it.initCause(lastError) }
	}

	private fun open(host: String, method: String, path: String): HttpsURLConnection {
		val c = URL("https://$host/api/v1$path").openConnection() as HttpsURLConnection
		c.sslSocketFactory = socketFactory
		c.hostnameVerifier = verifier
		c.connectTimeout = 5000
		c.readTimeout = 5000
		c.requestMethod = method
		c.setRequestProperty("Authorization", "Bearer ${pairing.token}")
		c.setRequestProperty("Accept", "application/json")
		return c
	}

	fun ping(): Ping = Parse.ping(call("GET", "/ping"))
	fun state(): FleetState = Parse.state(call("GET", "/state"))
	fun transcript(agentId: String, from: Int): Transcript =
		Parse.transcript(call("GET", "/agents/${enc(agentId)}/transcript?from=$from"))

	fun start(agentId: String, prompt: String) =
		call("POST", "/agents/${enc(agentId)}/start", JSONObject().put("prompt", prompt)).let { }

	fun send(agentId: String, text: String) =
		call("POST", "/agents/${enc(agentId)}/send", JSONObject().put("text", text)).let { }

	/** [action] is one of hold, resume, stop, kill. */
	fun agentAction(agentId: String, action: String) =
		call("POST", "/agents/${enc(agentId)}/$action").let { }

	fun decide(approvalId: String, allow: Boolean, reason: String = "") =
		call("POST", "/approvals/${enc(approvalId)}", JSONObject().put("allow", allow).put("reason", reason)).let { }

	fun killAll(): Int = call("POST", "/kill-all").optInt("killed")

	private fun enc(s: String) = java.net.URLEncoder.encode(s, "UTF-8").replace("+", "%20")
}

data class Ping(val name: String, val version: String, val demo: Boolean)

data class Fleet(val id: String, val name: String, val budget: Double, val spent: Double, val agents: Int, val live: Int)

data class Agent(
	val id: String, val name: String, val fleetId: String, val fleetName: String,
	val provider: String, val model: String, val status: String, val task: String, val lastTool: String,
	val sessionCost: Double, val cost: Double, val cap: Double, val error: String, val pending: Int,
) {
	/** Whether a prompt starts a new session (as opposed to being sent to a live one). */
	val canStart get() = status in setOf("idle", "stopped", "error", "capped")
	val canSend get() = status == "waiting" || status == "running" || status == "approval"
	val live get() = status in setOf("starting", "running", "waiting", "approval", "held")
}

data class Approval(
	val id: String, val agentId: String, val agentName: String, val tool: String,
	val summary: String, val input: String, val at: String,
)

data class FleetState(
	val seq: Long, val name: String, val version: String, val demo: Boolean, val spent: Double,
	val fleets: List<Fleet>, val agents: List<Agent>, val approvals: List<Approval>,
)

data class Entry(val at: String, val kind: String, val text: String, val tool: String, val isError: Boolean, val user: Boolean)

data class Transcript(val entries: List<Entry>, val next: Int)

/** JSON to model, tolerant of missing fields so a newer PC does not break an older phone. */
object Parse {
	private inline fun <T> JSONArray?.mapObjects(f: (JSONObject) -> T): List<T> {
		if (this == null) return emptyList()
		return (0 until length()).mapNotNull { optJSONObject(it) }.map(f)
	}

	fun ping(j: JSONObject) = Ping(j.s("name"), j.s("version"), j.optBoolean("demo"))

	fun state(j: JSONObject) = FleetState(
		seq = j.optLong("seq"), name = j.s("name"), version = j.s("version"),
		demo = j.optBoolean("demo"), spent = j.optDouble("spent_usd", 0.0),
		fleets = j.optJSONArray("fleets").mapObjects {
			Fleet(
				it.s("id"), it.s("name"), it.optDouble("budget_usd", 0.0),
				it.optDouble("spent_usd", 0.0), it.optInt("agents"), it.optInt("live"),
			)
		},
		agents = j.optJSONArray("agents").mapObjects {
			Agent(
				it.s("id"), it.s("name"), it.s("fleet_id"), it.s("fleet_name"),
				it.s("provider"), it.s("model"), it.s("status"),
				it.s("task"), it.s("last_tool"), it.optDouble("session_cost_usd", 0.0),
				it.optDouble("cost_usd", 0.0), it.optDouble("cap_usd", 0.0), it.s("error"), it.optInt("pending"),
			)
		},
		approvals = j.optJSONArray("approvals").mapObjects {
			Approval(
				it.s("id"), it.s("agent_id"), it.s("agent_name"), it.s("tool"),
				it.s("summary"), it.s("input"), it.s("at"),
			)
		},
	)

	fun transcript(j: JSONObject) = Transcript(
		j.optJSONArray("entries").mapObjects {
			Entry(
				it.s("at"), it.s("kind"), it.s("text"), it.s("tool"),
				it.optBoolean("is_error"), it.optBoolean("user"),
			)
		},
		j.optInt("next"),
	)
}

/** optString that reads a JSON null as empty rather than the text "null". */
fun JSONObject.s(key: String): String = if (isNull(key)) "" else optString(key)
