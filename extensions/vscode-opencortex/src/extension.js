const vscode = require("vscode");
const { OpenCortexClient } = require("./opencortexClient");
const { shouldAutoInject, normalizePriority } = require("./policy");

let client;
let statusBar;
const activeClaims = new Map();

function readConfig() {
  const cfg = vscode.workspace.getConfiguration("opencortex");
  return {
    serverUrl: cfg.get("serverUrl", "http://localhost:8080"),
    apiKey: cfg.get("apiKey", ""),
    agentName: cfg.get("agentName", ""),
    agentTags: cfg.get("agentTags", ["vscode"]),
    autoInjectPriority: cfg.get("autoInjectPriority", "high"),
    topics: cfg.get("topics", []),
    reconnectIntervalMs: cfg.get("reconnectIntervalMs", 2000),
  };
}

async function activate(context) {
  const cfg = readConfig();
  if (!cfg.apiKey) {
    vscode.window.showWarningMessage("OpenCortex: configure opencortex.apiKey to activate the bridge.");
    return;
  }

  const persisted = context.globalState.get("opencortex.clientId");
  const clientId = persisted || `vscode-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`;
  if (!persisted) {
    await context.globalState.update("opencortex.clientId", clientId);
  }

  client = new OpenCortexClient(cfg, console);

  statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  statusBar.text = "OpenCortex: connecting";
  statusBar.command = "opencortex.connect";
  statusBar.show();
  context.subscriptions.push(statusBar);

  const claimAndDispatch = async () => {
    const claims = await client.claim(5, 300);
    for (const claim of claims) {
      const message = claim.message || {};
      if (activeClaims.has(message.id)) {
        continue;
      }
      activeClaims.set(message.id, claim);
      await handleClaim(cfg, clientId, claim);
    }
  };

  const wsCallbacks = {
    onState: (state) => {
      statusBar.text = state === "connected" ? "OpenCortex: connected" : "OpenCortex: disconnected";
      if (state === "connected") {
        for (const topic of cfg.topics || []) {
          client.subscribeTopic(topic);
        }
      }
    },
    onHint: () => {
      claimAndDispatch().catch((err) => {
        console.warn("OpenCortex hint claim failed", err?.message || err);
      });
    },
    onMessage: () => {
      // Full message frames are already handled via claims.
    },
  };

  client.connectWS(wsCallbacks);
  client.startClaimLoop(claimAndDispatch, cfg.reconnectIntervalMs);

  const renewTimer = setInterval(() => {
    for (const [messageID, claim] of activeClaims.entries()) {
      client.renew(messageID, claim.claim_token, 300).catch((err) => {
        console.warn("OpenCortex renew failed", err?.message || err);
      });
    }
  }, 15000);

  context.subscriptions.push({
    dispose: () => {
      clearInterval(renewTimer);
      if (client) {
        client.dispose();
      }
    },
  });

  context.subscriptions.push(vscode.commands.registerCommand("opencortex.connect", async () => {
    client.dispose();
    client = new OpenCortexClient(cfg, console);
    client.connectWS(wsCallbacks);
    client.startClaimLoop(claimAndDispatch, cfg.reconnectIntervalMs);
    vscode.window.showInformationMessage("OpenCortex bridge reconnected.");
  }));

  context.subscriptions.push(vscode.commands.registerCommand("opencortex.claimNow", async () => {
    await claimAndDispatch();
    vscode.window.showInformationMessage("OpenCortex claim cycle completed.");
  }));

	context.subscriptions.push(vscode.commands.registerCommand("opencortex.markDone", async () => {
    const selected = await selectActiveClaim();
    if (!selected) {
      return;
    }
    const claim = selected.claim;
    const message = claim.message || {};
		const metadata = message.metadata || {};
		const replyTopic = metadata.reply_topic;
		if (replyTopic) {
			await publishResult(cfg, clientId, replyTopic, metadata, message);
		}
		await callToolWithFallback(
			"messages_ack",
			{ id: message.id, payload: { claim_token: claim.claim_token, mark_read: true } },
			() => client.ack(message.id, claim.claim_token, true)
		);
		activeClaims.delete(message.id);
		vscode.window.showInformationMessage(`OpenCortex: acknowledged ${message.id}`);
	}));

  context.subscriptions.push(vscode.commands.registerCommand("opencortex.nackClaim", async () => {
    const selected = await selectActiveClaim();
    if (!selected) {
      return;
		}
		const reason = await vscode.window.showInputBox({ prompt: "Reason for nack", value: "manual requeue" });
		await callToolWithFallback(
			"messages_nack",
			{ id: selected.claim.message.id, payload: { claim_token: selected.claim.claim_token, reason: reason || "manual requeue" } },
			() => client.nack(selected.claim.message.id, selected.claim.claim_token, reason || "manual requeue")
		);
		activeClaims.delete(selected.claim.message.id);
		vscode.window.showInformationMessage(`OpenCortex: requeued ${selected.claim.message.id}`);
	}));
}

async function handleClaim(cfg, clientId, claim) {
  const message = claim.message || {};
  const priority = normalizePriority(message.priority);
  const threshold = normalizePriority(cfg.autoInjectPriority);

  if (shouldAutoInject(priority, threshold)) {
    await injectIntoChat(message);
    return;
  }

  const action = await vscode.window.showInformationMessage(
    `OpenCortex task from ${message.from_agent_id || "unknown"}: ${truncate(message.content || "", 90)}`,
    "Inject Now",
    "Later",
    "Nack"
  );

  if (action === "Inject Now") {
    await injectIntoChat(message);
  }
	if (action === "Nack") {
		await callToolWithFallback(
			"messages_nack",
			{ id: message.id, payload: { claim_token: claim.claim_token, reason: "user dismissed task" } },
			() => client.nack(message.id, claim.claim_token, "user dismissed task")
		);
		activeClaims.delete(message.id);
	}
}

async function injectIntoChat(message) {
  const metadata = message.metadata || {};
  const contextFiles = Array.isArray(metadata.context_files) ? metadata.context_files : [];
  for (const file of contextFiles) {
    try {
      const uri = vscode.Uri.file(file);
      const doc = await vscode.workspace.openTextDocument(uri);
      await vscode.window.showTextDocument(doc, { preview: true, preserveFocus: true });
    } catch {
      // Ignore invalid context files.
    }
  }

  const prompt = buildPrompt(message);
  try {
    await vscode.commands.executeCommand("vscode.editorChat.start", { message: prompt });
    return;
  } catch {
    // Continue to fallback.
  }

  try {
    await vscode.commands.executeCommand("workbench.action.chat.open");
    await vscode.env.clipboard.writeText(prompt);
    vscode.window.showInformationMessage("OpenCortex prompt copied to clipboard for chat injection.");
  } catch {
    vscode.window.showWarningMessage("OpenCortex: unable to open chat automatically.");
  }
}

function buildPrompt(message) {
  const metadata = message.metadata || {};
  const replyTopic = metadata.reply_topic || "(none)";
  return [
    `[OpenCortex Message ${message.id}]`,
    `From: ${message.from_agent_id || "unknown"}`,
    `Priority: ${message.priority || "normal"}`,
    "",
    String(message.content || ""),
    "",
    `When complete, publish results to reply_topic '${replyTopic}' and acknowledge claim for message '${message.id}'.`,
  ].join("\n");
}

async function publishResult(cfg, clientId, replyTopic, metadata, message) {
	const text = `OpenCortex bridge completion for ${message.id}.\n\nPlease replace this with model output if needed.`;
	const payload = {
		topic_id: replyTopic,
		content_type: "text/markdown",
		content: text,
		priority: "normal",
		metadata: {
			correlation_id: metadata.correlation_id,
			workflow_id: metadata.workflow_id,
			injected_by: "opencortex-vscode",
			client_id: clientId,
			model_hint: "copilot|codex",
			source_message_id: message.id,
		},
		tags: cfg.agentTags || [],
	};
	return callToolWithFallback("messages_publish", { payload }, () => client.publish(payload));
}

async function callToolWithFallback(toolName, args, fallback) {
	try {
		const output = await vscode.commands.executeCommand("opencortex.mcp.call", {
			name: toolName,
			arguments: args || {},
		});
		if (output !== undefined) {
			return output;
		}
	} catch {
		// Fallback to REST client if MCP bridge command is unavailable.
	}
	return fallback();
}

async function selectActiveClaim() {
  if (activeClaims.size === 0) {
    vscode.window.showInformationMessage("OpenCortex: no active claims.");
    return null;
  }

  const items = [...activeClaims.values()].map((claim) => ({
    label: claim.message?.id || "(unknown)",
    description: truncate(claim.message?.content || "", 80),
    claim,
  }));

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: "Select an active claim",
  });
  return picked || null;
}

function truncate(value, max) {
  const text = String(value || "");
  if (text.length <= max) {
    return text;
  }
  return `${text.slice(0, max - 3)}...`;
}

function deactivate() {
  if (client) {
    client.dispose();
  }
  if (statusBar) {
    statusBar.dispose();
  }
}

module.exports = {
  activate,
  deactivate,
};
