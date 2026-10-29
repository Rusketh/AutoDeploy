package portal

import "strings"

// Deployment wording. Templates never print the stored outcome / phase
// tokens (in_progress, first-logon…); these helpers turn them into the
// plain labels operators read.

// outcomeLabel is a deployment outcome as a plain label.
func outcomeLabel(outcome string) string {
	switch outcome {
	case "ok":
		return "Succeeded"
	case "failed":
		return "Failed"
	case "in_progress":
		return "In progress"
	case "":
		return "Unknown"
	}
	return humanizeToken(outcome)
}

// outcomeBadge is the .badge colour class for an outcome.
func outcomeBadge(outcome string) string {
	switch outcome {
	case "ok":
		return "ok"
	case "failed":
		return "bad"
	}
	return "warn"
}

// outcomeDot is the .dot colour class for an outcome.
func outcomeDot(outcome string) string {
	switch outcome {
	case "ok":
		return "ok"
	case "failed":
		return "bad"
	case "in_progress":
		return "accent"
	}
	return "muted"
}

// phaseLabel names the stage a deployment reached in plain words. An
// empty phase (rows from before phases were recorded) reads "Starting"
// while the run is open and "Not recorded" once it is done.
func phaseLabel(phase, outcome string) string {
	switch phase {
	case "staging":
		return "Copying Windows media"
	case "staged":
		return "Rebooting into Windows Setup"
	case "specialize":
		return "Windows Setup"
	case "first-logon":
		return "First logon"
	case "agent-online":
		return "Agent started"
	case "installing":
		return "Installing software"
	case "complete":
		return "Complete"
	case "failed":
		return "Stopped by an error"
	case "":
		if outcome == "in_progress" {
			return "Starting"
		}
		return "Not recorded"
	}
	return humanizeToken(phase)
}

// kbStatusLabel is a Windows Update status as a plain label.
func kbStatusLabel(status string) string {
	switch status {
	case "installed":
		return "Installed"
	case "pending":
		return "Pending"
	case "failed":
		return "Failed"
	case "":
		return "Unknown"
	}
	return humanizeToken(status)
}

// humanizeToken turns an unknown snake/kebab-case token into words, so a
// value added later still reads as text: "needs_reboot" -> "Needs reboot".
func humanizeToken(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(s))
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// deployFuncs are the template helpers above.
func deployFuncs() map[string]any {
	return map[string]any{
		"outcomeLabel":  outcomeLabel,
		"outcomeBadge":  outcomeBadge,
		"outcomeDot":    outcomeDot,
		"phaseLabel":    phaseLabel,
		"kbStatusLabel": kbStatusLabel,
	}
}
