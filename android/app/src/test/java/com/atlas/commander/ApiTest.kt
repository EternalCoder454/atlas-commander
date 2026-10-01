package com.atlas.commander

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.security.cert.CertificateException

private const val FP = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

class PairingLinkTest {
	@Test
	fun parsesTheDocExampleLink() {
		// The phone must read exactly what the PC writes, including encoded names.
		val p = PairingLink.parse("atlascommander://pair?v=1&n=zach%20pc&h=192.168.1.5%3A47821%2C100.64.0.2%3A47821&t=abc_-123abc_-123&f=$FP")
		assertNotNull(p)
		assertEquals("zach pc", p!!.name)
		assertEquals(listOf("192.168.1.5:47821", "100.64.0.2:47821"), p.hosts)
		assertEquals("abc_-123abc_-123", p.token)
		assertEquals(FP, p.fingerprint)
	}

	@Test
	fun rejectsLinksThatCannotWork() {
		// A bad link must fail before anything is saved or any request is made.
		assertNull(PairingLink.parse("https://example.com/pair?v=1"))
		assertNull(PairingLink.parse("atlascommander://pair?v=2&h=a:1&t=x&f=$FP"))
		assertNull(PairingLink.parse("atlascommander://pair?v=1&h=a:1&t=x&f=abc"))
		assertNull(PairingLink.parse("atlascommander://pair?v=1&h=a:1&f=$FP"))
		assertNull(PairingLink.parse("atlascommander://pair?v=1&t=x&f=$FP"))
		assertNull(PairingLink.parse("not a link"))
	}

	private val goodToken = "abcdefghijklmnop"

	private fun link(h: String, t: String = goodToken) =
		PairingLink.parse("atlascommander://pair?v=1&n=pc&h=${java.net.URLEncoder.encode(h, "UTF-8")}&t=$t&f=$FP")

	@Test
	fun rejectsHostsThatAreNotPlainHostAndPort() {
		// A host with userinfo, a path or a query would change where the token is sent.
		for (h in listOf("a@b:1", "x/..:1", "h:1?x", "h:0", "h:70000", "h", ":1", "h:1x", "[::1:1")) {
			assertNull("host $h", link(h))
		}
	}

	@Test
	fun acceptsIpv4Ipv6AndDnsHosts() {
		// The PC lists LAN, Tailscale and IPv6 addresses; all must pass.
		assertNotNull(link("[fd00::1]:47821"))
		assertNotNull(link("192.168.1.5:47821"))
		assertNotNull(link("pc.local:47821"))
	}

	@Test
	fun rejectsBadTokensAndTooManyHosts() {
		// Tokens are random URL-safe text of known length; anything else is not from Commander.
		assertNull(link("a:1", "short"))
		assertNull(link("a:1", "abcdefghijklmnop%20"))
		assertNull(link("a:1", "a".repeat(129)))
		assertNull(link((1..9).joinToString(",") { "h$it:1" }))
		assertNotNull(link((1..8).joinToString(",") { "h$it:1" }))
	}

	@Test
	fun capsAndCleansTheName() {
		// The name is shown in dialogs, so control characters and length are limited.
		val n = "a\u0007b" + "c".repeat(100)
		val p = PairingLink.parse("atlascommander://pair?v=1&n=${java.net.URLEncoder.encode(n, "UTF-8")}&h=a:1&t=$goodToken&f=$FP")
		assertEquals(64, p!!.name.length)
		assertFalse(p.name.contains('\u0007'))
	}

	@Test
	fun remembersTheHostThatWorked() {
		// The first host that answers is tried first next time.
		val p = Pairing("pc", listOf("a:1", "b:1", "c:1"), "t", FP, lastHost = "b:1")
		assertEquals(listOf("b:1", "a:1", "c:1"), p.hostsToTry())
	}
}

class PinTest {
	@Test
	fun fingerprintIsLowercaseHexSha256() {
		// Known SHA-256 of "abc"; the PC computes the pin the same way.
		assertEquals(
			"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
			Pin.fingerprint("abc".toByteArray()),
		)
	}

	@Test
	fun matchesOnlyTheExactCertificate() {
		val der = "certificate".toByteArray()
		val pin = Pin.fingerprint(der)
		assertTrue(Pin.matches(der, pin))
		assertTrue(Pin.matches(der, pin.uppercase()))
		assertFalse(Pin.matches("other".toByteArray(), pin))
	}

	@Test
	fun trustManagerRefusesWithoutAChain() {
		// With no certificate there is nothing to pin, so it must fail rather than trust.
		val tm = PinTrustManager(FP)
		try {
			tm.checkServerTrusted(emptyArray(), "ECDHE_ECDSA")
			fail("expected a CertificateException")
		} catch (_: CertificateException) {
		}
		try {
			tm.checkServerTrusted(null, "ECDHE_ECDSA")
			fail("expected a CertificateException")
		} catch (_: CertificateException) {
		}
	}
}

class ParseTest {
	private val example = """
	{
	  "seq": 812, "name": "zach-pc", "version": "0.1.0", "demo": false, "spent_usd": 1.84,
	  "fleets": [{"id": "f1", "name": "Website", "budget_usd": 20, "spent_usd": 3.1, "agents": 3, "live": 1}],
	  "agents": [{"id": "a1", "name": "Builder", "fleet_id": "f1", "fleet_name": "Website",
	    "provider": "claude-code", "model": "sonnet", "status": "running",
	    "task": "Fix the login form", "last_tool": "Bash: go test ./...",
	    "session_cost_usd": 0.42, "cost_usd": 2.9, "cap_usd": 5,
	    "error": "", "pending": 1, "started_at": "2026-10-01T07:00:00Z"}],
	  "approvals": [{"id": "p9", "agent_id": "a1", "agent_name": "Builder", "tool": "Bash",
	    "summary": "rm -rf build", "input": "{\n  \"command\": \"rm -rf build\"\n}",
	    "at": "2026-10-01T07:03:10Z"}]
	}
	""".trimIndent()

	@Test
	fun parsesTheDocStateExample() {
		val s = Parse.state(JSONObject(example))
		assertEquals(812L, s.seq)
		assertEquals("zach-pc", s.name)
		assertEquals(1.84, s.spent, 0.0001)
		assertEquals("Website", s.fleets[0].name)
		assertEquals(20.0, s.fleets[0].budget, 0.0)
		val a = s.agents[0]
		assertEquals("running", a.status)
		assertEquals("Bash: go test ./...", a.lastTool)
		assertEquals(5.0, a.cap, 0.0)
		assertEquals(1, a.pending)
		assertTrue(a.canSend)
		assertFalse(a.canStart)
		val p = s.approvals[0]
		assertEquals("p9", p.id)
		assertEquals("{\n  \"command\": \"rm -rf build\"\n}", p.input)
	}

	@Test
	fun jsonNullReadsAsEmptyText() {
		// A null error must not show up on screen as the word "null".
		val s = Parse.state(JSONObject("""{"agents":[{"id":"a","status":"idle","error":null}]}"""))
		assertEquals("", s.agents[0].error)
		assertTrue(s.agents[0].canStart)
	}

	@Test
	fun parsesTranscript() {
		val t = Parse.transcript(
			JSONObject("""{"entries":[{"at":"x","kind":"text","text":"hi","tool":"","is_error":false,"user":true}],"next":57}"""),
		)
		assertEquals(57, t.next)
		assertTrue(t.entries[0].user)
		assertEquals("hi", t.entries[0].text)
	}
}
