package com.atlas.commander

import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import java.util.Locale

private fun usd(v: Double) = String.format(Locale.US, "$%.2f", v)

private fun costLine(a: Agent) =
	if (a.cap > 0) "${usd(a.cost)} / ${usd(a.cap)}" else usd(a.cost)

@Composable
fun Root(model: AppModel, scan: () -> Unit, setNotify: (Boolean) -> Unit, version: String) {
	Surface(color = MaterialTheme.colorScheme.background, modifier = Modifier.fillMaxSize()) {
		if (model.needsPairing) PairScreen(model, scan) else MainScreen(model, setNotify, version)
	}
}

// ---------------------------------------------------------------- pairing

@Composable
fun PairScreen(model: AppModel, scan: () -> Unit) {
	var link by rememberSaveable { mutableStateOf("") }
	Column(
		Modifier.fillMaxSize().statusBarsPadding().verticalScroll(rememberScrollState()).padding(24.dp),
		verticalArrangement = Arrangement.spacedBy(16.dp),
	) {
		Text("Atlas Commander", style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.SemiBold)
		model.repairMessage?.let {
			Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyLarge)
		}
		Text(
			"On your PC, open Commander → Settings → Phone, turn it on and scan the code.",
			style = MaterialTheme.typography.bodyLarge,
		)
		Button(onClick = scan, enabled = !model.pairBusy, modifier = Modifier.fillMaxWidth().height(52.dp)) {
			Text("Scan code")
		}
		Text("Or paste the link Commander shows under the code:", style = MaterialTheme.typography.bodyMedium)
		OutlinedTextField(
			value = link, onValueChange = { link = it }, label = { Text("Paste link") },
			placeholder = { Text("atlascommander://pair?…") }, minLines = 2, maxLines = 4,
			modifier = Modifier.fillMaxWidth(),
		)
		OutlinedButton(
			onClick = { model.pairFromText(link) }, enabled = link.isNotBlank() && !model.pairBusy,
			modifier = Modifier.fillMaxWidth().height(48.dp),
		) { Text("Pair") }
		if (model.pairBusy) {
			Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
				CircularProgressIndicator(Modifier.height(24.dp))
				Text("Connecting…")
			}
		}
		model.pairError?.let { Text(it, color = MaterialTheme.colorScheme.error) }
	}
}

// ---------------------------------------------------------------- main

private enum class Tab { Board, Approvals, Settings }

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MainScreen(model: AppModel, setNotify: (Boolean) -> Unit, version: String) {
	var tab by rememberSaveable { mutableStateOf(Tab.Board) }
	var agentId by rememberSaveable { mutableStateOf<String?>(null) }
	var confirmKill by remember { mutableStateOf(false) }
	val snack = remember { SnackbarHostState() }
	val state = model.state

	LaunchedEffect(model.toast) {
		model.toast?.let {
			snack.showSnackbar(it)
			model.toast = null
		}
	}
	BackHandler(agentId != null) { agentId = null }

	val agent = agentId?.let { id -> state?.agents?.firstOrNull { it.id == id } }
	val pendingCount = state?.approvals?.size ?: 0

	Scaffold(
		containerColor = MaterialTheme.colorScheme.background,
		snackbarHost = { SnackbarHost(snack) },
		topBar = {
			TopAppBar(
				navigationIcon = {
					if (agentId != null) {
						IconButton(onClick = { agentId = null }) {
							Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
						}
					}
				},
				title = {
					Column {
						Text(
							if (agentId != null) agent?.name ?: "Agent" else model.pcName(),
							maxLines = 1, overflow = TextOverflow.Ellipsis,
						)
						state?.let {
							Text(
								"Spent today ${usd(it.spent)}" + if (it.demo) " · demo" else "",
								style = MaterialTheme.typography.bodySmall,
								color = MaterialTheme.colorScheme.onSurfaceVariant,
							)
						}
					}
				},
				actions = {
					TextButton(onClick = { confirmKill = true }) {
						Text("Kill all", color = MaterialTheme.colorScheme.error)
					}
				},
			)
		},
		bottomBar = {
			NavigationBar {
				NavigationBarItem(
					selected = tab == Tab.Board, onClick = { tab = Tab.Board; agentId = null },
					icon = { Icon(Icons.Filled.Home, null) }, label = { Text("Board") },
				)
				NavigationBarItem(
					selected = tab == Tab.Approvals, onClick = { tab = Tab.Approvals; agentId = null },
					icon = {
						BadgedBox(badge = { if (pendingCount > 0) Badge { Text(pendingCount.toString()) } }) {
							Icon(Icons.Filled.Notifications, null)
						}
					},
					label = { Text("Approvals") },
				)
				NavigationBarItem(
					selected = tab == Tab.Settings, onClick = { tab = Tab.Settings; agentId = null },
					icon = { Icon(Icons.Filled.Settings, null) }, label = { Text("Settings") },
				)
			}
		},
	) { pad ->
		Column(Modifier.padding(pad).fillMaxSize()) {
			model.error?.let {
				Surface(color = MaterialTheme.colorScheme.error.copy(alpha = 0.16f), modifier = Modifier.fillMaxWidth()) {
					Text(it, Modifier.padding(12.dp), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium)
				}
			}
			Box(Modifier.weight(1f)) {
				when {
					agentId != null && agent != null -> AgentDetail(model, agent)
					agentId != null -> Empty("This agent is gone.")
					tab == Tab.Board -> Board(model) { agentId = it }
					tab == Tab.Approvals -> Approvals(model)
					else -> SettingsScreen(model, setNotify, version)
				}
			}
		}
	}

	if (confirmKill) {
		AlertDialog(
			onDismissRequest = { confirmKill = false },
			title = { Text("Kill all agents?") },
			text = { Text("This stops every running agent on ${model.pcName()} right away.") },
			confirmButton = {
				TextButton(onClick = { confirmKill = false; model.killAll() }) {
					Text("Kill all", color = MaterialTheme.colorScheme.error)
				}
			},
			dismissButton = { TextButton(onClick = { confirmKill = false }) { Text("Cancel") } },
		)
	}
}

@Composable
private fun Empty(text: String) {
	Box(Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
		Text(text, color = MaterialTheme.colorScheme.onSurfaceVariant)
	}
}

@Composable
fun StatusChip(status: String) {
	val c = LocalStatusColors.current.forStatus(status)
	Surface(shape = RoundedCornerShape(50), color = c.copy(alpha = 0.18f)) {
		Text(
			status, Modifier.padding(horizontal = 10.dp, vertical = 3.dp), color = c,
			style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.Medium,
		)
	}
}

private val cardColors @Composable get() =
	CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant)

// ---------------------------------------------------------------- board

@Composable
fun Board(model: AppModel, open: (String) -> Unit) {
	val s = model.state
	if (s == null) {
		Empty(if (model.error == null) "Loading…" else "Waiting for ${model.pcName()}…")
		return
	}
	if (s.agents.isEmpty()) {
		Empty("No agents yet. Add one in Commander on your PC.")
		return
	}
	// Fleets in the PC's order, then any agent whose fleet is not listed.
	val groups = LinkedHashMap<String, MutableList<Agent>>()
	s.fleets.forEach { groups[it.id] = mutableListOf() }
	s.agents.forEach { groups.getOrPut(it.fleetId) { mutableListOf() }.add(it) }
	LazyColumn(contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
		for ((fid, list) in groups) {
			if (list.isEmpty()) continue
			val fleet = s.fleets.firstOrNull { it.id == fid }
			item(key = "fleet-$fid") {
				Row(Modifier.fillMaxWidth().padding(top = 6.dp), horizontalArrangement = Arrangement.SpaceBetween) {
					Text(fleet?.name ?: list.first().fleetName, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
					if (fleet != null) {
						val budget = if (fleet.budget > 0) "${usd(fleet.spent)} / ${usd(fleet.budget)}" else usd(fleet.spent)
						Text("${fleet.live} live · $budget", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
					}
				}
			}
			items(list, key = { it.id }) { a ->
				Card(onClick = { open(a.id) }, colors = cardColors, modifier = Modifier.fillMaxWidth()) {
					Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
						Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
							Text(a.name, Modifier.weight(1f), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
							StatusChip(a.status)
						}
						val line = a.lastTool.ifBlank { a.task }
						if (line.isNotBlank()) {
							Text(line, maxLines = 2, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
						}
						Row(horizontalArrangement = Arrangement.SpaceBetween, modifier = Modifier.fillMaxWidth()) {
							Text(costLine(a), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
							if (a.pending > 0) Text("${a.pending} waiting for approval", style = MaterialTheme.typography.bodySmall, color = LocalStatusColors.current.warn)
						}
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------- detail

@Composable
fun AgentDetail(model: AppModel, a: Agent) {
	val owner = LocalContext.current as ComponentActivity
	val entries = remember(a.id) { mutableStateListOf<Entry>() }
	var next by remember(a.id) { mutableStateOf(0) }
	var text by rememberSaveable(a.id) { mutableStateOf("") }
	val list = rememberLazyListState()

	LaunchedEffect(a.id) {
		owner.repeatOnLifecycle(Lifecycle.State.STARTED) {
			while (true) {
				val c = model.api()
				if (c != null) {
					try {
						val t = withContext(Dispatchers.IO) { c.transcript(a.id, next) }
						// A smaller index means the PC restarted its log: start over.
						if (t.next < next) entries.clear()
						entries.addAll(t.entries)
						next = t.next
					} catch (_: Exception) {
						// The state poll already shows why the PC is unreachable.
					}
				}
				delay(2000)
			}
		}
	}
	LaunchedEffect(entries.size) {
		if (entries.isNotEmpty()) list.animateScrollToItem(entries.size - 1)
	}

	Column(Modifier.fillMaxSize().imePadding()) {
		Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
			Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
				StatusChip(a.status)
				Text(costLine(a), style = MaterialTheme.typography.bodyMedium)
				Text("${a.provider} ${a.model}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
			}
			if (a.error.isNotBlank()) Text(a.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium)
			Row(horizontalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.fillMaxWidth()) {
				val m = Modifier.weight(1f)
				val pad = PaddingValues(horizontal = 4.dp)
				OutlinedButton({ model.act { agentAction(a.id, "hold") } }, m, enabled = a.live && a.status != "held", contentPadding = pad) { Text("Hold") }
				OutlinedButton({ model.act { agentAction(a.id, "resume") } }, m, enabled = a.status == "held", contentPadding = pad) { Text("Resume") }
				OutlinedButton({ model.act { agentAction(a.id, "stop") } }, m, enabled = a.live, contentPadding = pad) { Text("Stop") }
				OutlinedButton(
					{ model.act { agentAction(a.id, "kill") } }, m, enabled = a.live, contentPadding = pad,
					colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
				) { Text("Kill") }
			}
		}
		LazyColumn(
			state = list, modifier = Modifier.weight(1f).fillMaxWidth(),
			contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp),
		) {
			if (entries.isEmpty()) item { Text("No transcript yet.", color = MaterialTheme.colorScheme.onSurfaceVariant) }
			items(entries.size) { EntryRow(entries[it]) }
		}
		Row(Modifier.padding(12.dp), verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
			OutlinedTextField(
				value = text, onValueChange = { text = it }, modifier = Modifier.weight(1f), maxLines = 4,
				placeholder = { Text(if (a.canStart) "Task for ${a.name}" else "Message") },
			)
			val enabled = text.isNotBlank() && (a.canStart || a.canSend)
			Button(
				onClick = {
					val t = text.trim()
					text = ""
					if (a.canStart) model.act { start(a.id, t) } else model.act { send(a.id, t) }
				},
				enabled = enabled, modifier = Modifier.height(56.dp),
			) { Text(if (a.canStart) "Start" else "Send") }
		}
	}
}

@Composable
private fun EntryRow(e: Entry) {
	val scheme = MaterialTheme.colorScheme
	val (color, bg) = when {
		e.user -> scheme.primary to scheme.primary.copy(alpha = 0.14f)
		e.isError || e.kind == "error" -> scheme.error to scheme.error.copy(alpha = 0.12f)
		e.kind == "tool" || e.kind == "result" -> scheme.onSurfaceVariant to scheme.surfaceVariant
		e.kind == "info" -> scheme.onSurfaceVariant to scheme.background
		else -> scheme.onSurface to scheme.background
	}
	Surface(color = bg, shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()) {
		Column(Modifier.padding(10.dp)) {
			val mono = e.kind == "tool" || e.kind == "result"
			if (e.kind == "tool" && e.tool.isNotBlank()) {
				Text(e.tool, style = MaterialTheme.typography.labelMedium, color = color, fontWeight = FontWeight.SemiBold)
			}
			Text(
				e.text, color = color, style = MaterialTheme.typography.bodyMedium,
				fontFamily = if (mono) FontFamily.Monospace else FontFamily.Default,
				fontSize = if (mono) 12.sp else 14.sp,
			)
		}
	}
}

// ---------------------------------------------------------------- approvals

@Composable
fun Approvals(model: AppModel) {
	val list = model.state?.approvals.orEmpty()
	var denying by remember { mutableStateOf<Approval?>(null) }
	if (list.isEmpty()) {
		Empty("Nothing is waiting for approval.")
	} else {
		LazyColumn(contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
			items(list, key = { it.id }) { a ->
				var expanded by remember { mutableStateOf(false) }
				Card(colors = cardColors, modifier = Modifier.fillMaxWidth()) {
					Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
						Text("${a.agentName} wants to use ${a.tool}", style = MaterialTheme.typography.titleMedium)
						Text(a.summary, style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace)
						if (a.input.isNotBlank()) {
							TextButton({ expanded = !expanded }, contentPadding = PaddingValues(0.dp)) {
								Text(if (expanded) "Hide input" else "Show input")
							}
							if (expanded) {
								Surface(color = MaterialTheme.colorScheme.background, shape = RoundedCornerShape(8.dp)) {
									Text(
										a.input, Modifier.fillMaxWidth().padding(10.dp),
										fontFamily = FontFamily.Monospace, fontSize = 12.sp,
									)
								}
							}
						}
						Row(horizontalArrangement = Arrangement.spacedBy(12.dp), modifier = Modifier.fillMaxWidth()) {
							Button({ model.act { decide(a.id, true) } }, Modifier.weight(1f).height(56.dp)) { Text("Allow", fontSize = 16.sp) }
							OutlinedButton(
								{ denying = a }, Modifier.weight(1f).height(56.dp),
								colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
							) { Text("Deny", fontSize = 16.sp) }
						}
					}
				}
			}
		}
	}
	denying?.let { a ->
		var reason by remember { mutableStateOf("") }
		AlertDialog(
			onDismissRequest = { denying = null },
			title = { Text("Deny ${a.tool}?") },
			text = {
				OutlinedTextField(reason, { reason = it }, label = { Text("Reason (optional)") }, modifier = Modifier.fillMaxWidth())
			},
			confirmButton = {
				TextButton(onClick = {
					denying = null
					model.act { decide(a.id, false, reason.trim()) }
				}) { Text("Deny", color = MaterialTheme.colorScheme.error) }
			},
			dismissButton = { TextButton(onClick = { denying = null }) { Text("Cancel") } },
		)
	}
}

// ---------------------------------------------------------------- settings

@Composable
fun SettingsScreen(model: AppModel, setNotify: (Boolean) -> Unit, version: String) {
	var confirm by remember { mutableStateOf(false) }
	val p = model.pairing
	Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
		Card(colors = cardColors, modifier = Modifier.fillMaxWidth()) {
			Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
				Text("Paired PC", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
				Text(model.pcName(), style = MaterialTheme.typography.titleMedium)
				p?.let { Text(it.hostsToTry().joinToString(", "), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
				model.state?.let { Text("Commander ${it.version}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
				OutlinedButton({ confirm = true }, Modifier.padding(top = 8.dp)) { Text("Unpair") }
			}
		}
		Card(colors = cardColors, modifier = Modifier.fillMaxWidth()) {
			Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
				Column(Modifier.weight(1f)) {
					Text("Notify me about approvals", style = MaterialTheme.typography.titleMedium)
					Text("Keeps a small notification while it watches your PC.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
				}
				Switch(checked = model.notifyEnabled, onCheckedChange = setNotify)
			}
		}
		Text("Atlas Commander $version", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
	}
	if (confirm) {
		AlertDialog(
			onDismissRequest = { confirm = false },
			title = { Text("Unpair from ${model.pcName()}?") },
			text = { Text("You will need to scan the code again to reconnect.") },
			confirmButton = { TextButton({ confirm = false; model.unpair() }) { Text("Unpair", color = MaterialTheme.colorScheme.error) } },
			dismissButton = { TextButton({ confirm = false }) { Text("Cancel") } },
		)
	}
}
