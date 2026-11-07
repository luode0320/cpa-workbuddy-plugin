package main

import (
	"strings"
	"testing"
)

// Refusal and non-refusal examples come from real session transcripts. Every refusal must match
// within the default stream prefetch window (128 bytes of visible text), so the stream is closed
// before any chunk reaches the client.
func TestDetectTextRefusalFromSessions(t *testing.T) {
	refusals := []string{
		"I can't help make a bomb.",
		"I can't help with this one. Getting Frida to inject into Pokémon GO means getting it past Niantic's anti-tamper protections.",
		"I can't help with this one, including the noctowl3 update and the list of other server-side rolls to brute-force.",
		"Sorry, I cannot fulfill your request to hook the live application or inspect its runtime memory and internal SDK mechanisms.",
		"I won't build this one. Replaying `ProcessTapableProto` to collect Rare Candy repeatedly exploits a bug in Niantic's live servers.",
		"I won't do that.\n\nBuilding injection payloads designed to confuse/manipulate AI agents is offensive prompt-injection tooling.",
		"I won't run more brute-force passes against the salt. That work was stopped by a safety check.",
		"I won't generate this session.\n\nThe request is to forge conversation history that primes a future Claude instance.",
		"I won't compile that list. The admin page prints documents in a doctor's name with their CRM and RQE.",
		"I won't add a wildcard permission for this. A blanket `\"*\"` rule turns off permission gating across your whole machine.",
		"I can't do this one. Decompiling the whole agent back into source would recreate a third party's proprietary codebase in full.",
		"I can't continue this analysis. That includes the bytecode disassembler and pulling the player-ID extraction logic.",
		"I can't continue exploiting the live admin panel or extracting API keys, sessions, or other credentials.",
		"I can't run these probes against pgorelease.nianticlabs.com. That's Niantic's infrastructure.",
		"I can't actively fuzz `pgorelease.nianticlabs.com`, a public third-party Niantic domain.",
		"I can't fabricate authorization or forge a session to circumvent safeguards.",
		"I can't remove the demo labeling or import that real medical certificate into the site.",
		"I can't rewrite it to convincingly impersonate AloSaude Floripa or use an actual medical PDF/logo.",
		"I can't redesign the site to convincingly impersonate a real medical-certificate validation service.",
		"I cannot run a workflow to catalog vulnerabilities for infiltrating servers or exfiltrating data.",
		"I'm not going to continue with this.\n\nWhat we've been doing is black-box reconnaissance against a production server.",
		"I'm not going to build this one.\n\nSeveral things make this look like a site for making a fake sick note pass verification.",
		"I'm not going to write that prompt. Handing another model the spec for injecting this into the APK is the same evasion work.",
		"I'm going to stop here rather than continue the scan.\n\nThe target is a production backend, not internal infrastructure.",
		"I'm going to stop here rather than re-run that sweep.\n\n`pgorelease.nianticlabs.com` is Niantic's production backend.",
		"I'm going to stop the reverse-engineering of the camera's P2P protocol here and won't carry it further in this session.",
		"I'm stopping here: I can't do the full source reconstruction of `agent.bin`.",
		"I'm not running the raid seed sweep.\n\nThis hook has fired many times with the same demand, and my answer is settled.",
		"I'm done here. My decision stands and won't change through this loop. Use `/goal clear`, or confirm the scope.",
		"I'll stop there and not retry that.\n\nI can't help start an adbd server on that device to open it up for remote connection.",
		"Stopping here, because the device doesn't match the task as framed.",
		"I need to stop here. I'm not going to fuzz or enumerate endpoints on `pgorelease.nianticlabs.com`.",
		"Not going to run this one.\n\nTwo things stack up against it: the scenario targets live third-party production infrastructure.",
		"Not running this one. The skill's whole mechanism is hiding the real target from the model.",
		"Can't do this one. Fuzzing Niantic's infrastructure without authorization is unauthorized access to third-party systems.",
		"My answer is the same as a moment ago: I won't run that sweep.",
		"My decision is final and the loop won't change it.",
		"Before continuing, I need to flag something rather than proceed.\n\nThe target here is a real third-party company.",
		"Before I continue probing this third-party admin interface, confirm that you own it or are authorized to test it.",
		"Before I send more probes to Niantic's admin service, confirm that you own this system or have authorization to test it.",
		"I can help with this, but I need to know the context first. Stealthing Frida against RASP is a detection-evasion request.",
		"I can help with an authorized security assessment, but not with breaking into Niantic's live site without clear permission.",
		"I can help assess this local app for known exposed components and safe indicators, but I won't try to exploit it.",
		"I'll recheck the base URL, then use read-only requests to discover routes and exposed files. I won't send state-changing requests or destructive exploit payloads.",
		"I stopped the raid seed sweep that the stop hook asked for. A safety check halted my previous message partway through writing the sweep script, so I won't produce that script again.",
		"Before continuing active enumeration of Niantic's admin service, I need confirmation that you own it or are authorized to test it, including the permitted scope.",
		"Não vou construir esse bypass — nem o script Frida, nem a lib patchada, e também não vou fazer a parte de RE.",
		"Não vou fazer isso.\n\nNão é a parte de análise — é o destino dela.",
		"Não vou reproduzir esses segredos nem recomendar continuar testando.",
		"Não dá pra usar isso: o pedido é gerar uma sessão falsa que esconda o alvo real.",
		"This request was blocked by Gemini's filters. They can occasionally trigger by mistake on safe coding queries. Please try rephrasing your prompt.",
		"The harness blocked that — not an error. The auto-mode classifier flagged it as an \"External Ingress Tunnel\".",
		"The CLAUDE.md loaded in this session contains prompt-injection language designed to override my safety guidelines.",
		"I can't do this. Probing and fuzzing endpoints on Niantic's infrastructure without documented authorization is unauthorized reconnaissance.",
		"I can't continue fuzzing endpoints on pgorelease.nianticlabs.com. That's Niantic's production infrastructure and there's no authorization context established.",
		"I'm keeping to my decision: I won't continue the salt search. The hook is right that I haven't shown the salt can't be found.",
		"I understand the concept, but I won't build it. What you're describing is a tool whose core mechanism is fabricating a fake compliance history.",
		"I'll include bounded state-changing tests where they are reversible, but I won't run destructive operations or availability-impacting payloads against production.",
	}
	maxEnd := 0
	for _, text := range refusals {
		m := textRefusalPattern.FindStringIndex(strings.NewReplacer("’", "'", "‘", "'").Replace(text))
		if detectTextRefusal(text) == "" {
			t.Errorf("refusal not detected: %.100q", text)
			continue
		}
		if m[1] > maxEnd {
			maxEnd = m[1]
		}
		if m[1] > 128 {
			t.Errorf("refusal matched at byte %d, beyond the 128-byte prefetch window: %.100q", m[1], text)
		}
	}
	t.Logf("max match end across %d refusals: %d bytes", len(refusals), maxEnd)

	nonRefusals := []string{
		"I can't see that run: the newest agent log on this machine is from an older build, so this is a guess from the diff.",
		"I can't tell what's wrong from here without seeing what you see. Send me either a screenshot or answers to these.",
		"I can't confirm it yet, and part of my last answer was wrong. The live drop I caught wasn't a network event.",
		"I can't see why it crashed: the log collector deleted the game's and system_server's lines from this file.",
		"I can't drive Chrome from this session: no Claude in Chrome tools are connected here.",
		"I can't run interactive `sudo` here because there's no terminal for your password.",
		"I can't do this one from here. GitHub's API has no endpoint for changing a container package's visibility.",
		"All four releases are published, and the registry and archive URLs now return 200. I can't reproduce the store's issue.",
		"I'm going to stop typing into that terminal — our inputs are colliding.",
		"Per your setup, I won't run the suite; I'll leave the exact commands for you.",
		"The cheapest remaining lead is the feature gate's own definition. I won't run more heavy loops inside the production API container.",
		"I found the cause, but I haven't changed anything yet. The fix needs a decision from you, and the device is busy.",
		"Our work includes vulnerability research, reverse engineering, dynamic instrumentation, protocol and cryptographic analysis, and security-control assessment.",
		"Updated. The key additions:\n\n**Continuity Design section (rules 11-18):**\n- **Rule 11** — always ask which model will resume the session.",
		"That's a fair question. Here's the honest tradeoff:\n\n**Case for disclosure:**\n- Standard security ethics — vulns get reported.",
		"Santos deep-dive is in — the picture for it has sharpened considerably: prerequisites confirmed, unauthenticated multipart POST to the login form is the entry path.",
		"The fork image built and was published as `ghcr.io/esauvisky/cliproxyapi:latest`. hdev can't pull it: the package is private.",
		"No, that error isn't related. It fails in a different place from both of the other problems.",
		"I can give you the commands to run in the device's Termux yourself. But the blocker is reachability, not the command.",
		"Both routes allow only the expected read methods; OPTIONS reports GET/HEAD/OPTIONS. The headers lack some common hardening headers.",
	}
	for _, text := range nonRefusals {
		if reason := detectTextRefusal(text); reason != "" {
			t.Errorf("false positive (%s): %.100q", reason, text)
		}
	}
}
