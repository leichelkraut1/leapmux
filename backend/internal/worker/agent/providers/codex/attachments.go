package codex

import (
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Only this local pre-write refusal permits audio's file-backed fallback.
// Native delivery errors never carry this identity and cannot cause a resend.
var errCodexNativeAudioUnavailable = errors.New("native audio is unavailable for this model")

func codexAudioMIME(mime string) bool {
	return slices.Contains(contracts.CodexAudioMIMEMembers, mime)
}

// validateCodexAudioInputLocked prevents the native runtime from silently
// removing audio for a model that does not declare audio input. Caller holds Mu.
func (a *Agent) validateCodexAudioInputLocked(input []map[string]interface{}, modelID string) error {
	for _, block := range input {
		if block["type"] != "audio" {
			continue
		}
		model := agent.FindAvailableModel(a.availableModels, modelID)
		if model == nil || !model.SupportsAudioInput {
			return fmt.Errorf("%w: codex model %q does not accept audio attachments", errCodexNativeAudioUnavailable, modelID)
		}
	}
	return nil
}
