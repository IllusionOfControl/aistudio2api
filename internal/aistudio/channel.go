package aistudio

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Channel represents upstream quota source for generation requests
type Channel string

const (
	// ChannelPlayground represents GenerateContent on official Playground
	ChannelPlayground Channel = "playground"
	// ChannelBuild represents Gemini API calls proxied by official Build app
	ChannelBuild Channel = "build"
)

// ChannelCooldownScope returns channel cooldown key for scope; Build uses build:<scope>
func ChannelCooldownScope(channel Channel, scope string) string {
	scope = strings.TrimSpace(scope)
	if channel == ChannelBuild && scope != "" {
		return ModelAccessKey(string(ChannelBuild), scope)
	}
	return scope
}

// channelCandidate represents an account and channel combination
type channelCandidate struct {
	index   int
	channel Channel
}

// SetUpstreamChannels sets upstream channels enabled in order for generation requests
func (p *AccountPool) SetUpstreamChannels(channels []Channel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.channels = append([]Channel(nil), channels...)
	p.notifyLocked()
}

// SetBuildCatalog saves Gemini API model catalog returned by account Build proxy
func (p *AccountPool) SetBuildCatalog(accountID string, models []Model) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.byID[strings.TrimSpace(accountID)]
	if account == nil {
		return ErrAccountNotFound
	}
	account.buildModels = cloneAccountModels(models)
	p.notifyLocked()
	return nil
}

// BuildEnabled returns whether Build channel is enabled
func (p *AccountPool) BuildEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.channelEnabledLocked(ChannelBuild)
}

// Channel returns upstream channel used by lease
func (l *AccountLease) Channel() Channel {
	if l == nil || l.channel == "" {
		return ChannelPlayground
	}
	return l.channel
}

// CooldownScope returns lease channel cooldown key for model scope
func (l *AccountLease) CooldownScope(scope string) string {
	return ChannelCooldownScope(l.Channel(), scope)
}

func (p *AccountPool) channelEnabledLocked(channel Channel) bool {
	if len(p.channels) == 0 {
		return channel == ChannelPlayground
	}
	return slices.Contains(p.channels, channel)
}

func (p *AccountPool) enabledChannelsLocked() []Channel {
	if len(p.channels) == 0 {
		return []Channel{ChannelPlayground}
	}
	return p.channels
}

// generationChannelSelection determines whether selection is a generation request scheduled by channel
func generationChannelSelection(selection AccountSelection) bool {
	return strings.TrimSpace(selection.ModelID) != "" && selection.Method == "generateContent" &&
		strings.TrimSpace(selection.ModelAccessScope) == "" && strings.TrimSpace(selection.ResourceID) == "" &&
		strings.TrimSpace(selection.Capability) == "" && !selection.PlaygroundOnly
}

// buildMethods lists upstream methods called via the Build proxy serving models absent from the Playground catalog.
var buildMethods = []string{"embedContent", "bidiGenerateContent", "bidiGenerateMusic"}

// buildMethodSelection checks whether the selection is an embedding or realtime method serving Build-exclusive models.
func buildMethodSelection(selection AccountSelection) bool {
	return strings.TrimSpace(selection.ModelID) != "" && slices.Contains(buildMethods, selection.Method) &&
		strings.TrimSpace(selection.Capability) == "" && !selection.PlaygroundOnly
}

// selectionChannelsLocked returns available channel order for selection, preferred channel first; embeddings use Build only, realtime methods try Playground then Build, other non-generation requests use Playground RPC only.
func (p *AccountPool) selectionChannelsLocked(selection AccountSelection) []Channel {
	if !generationChannelSelection(selection) {
		if buildMethodSelection(selection) && p.channelEnabledLocked(ChannelBuild) {
			if selection.Method == "embedContent" {
				return []Channel{ChannelBuild}
			}
			return []Channel{ChannelPlayground, ChannelBuild}
		}
		return []Channel{ChannelPlayground}
	}
	if selection.Channel != "" {
		if p.channelEnabledLocked(selection.Channel) {
			return []Channel{selection.Channel}
		}
		return nil
	}
	channels := p.enabledChannelsLocked()
	if selection.PreferredChannel == "" || !slices.Contains(channels, selection.PreferredChannel) {
		return channels
	}
	ordered := []Channel{selection.PreferredChannel}
	for _, channel := range channels {
		if channel != selection.PreferredChannel {
			ordered = append(ordered, channel)
		}
	}
	return ordered
}

// preferredChannelOpenLocked checks whether account preferred channel supports selection and is not cooling down
func (p *AccountPool) preferredChannelOpenLocked(account *Account, selection AccountSelection, now time.Time) bool {
	preferred := selection.PreferredChannel
	if preferred == "" || !slices.Contains(p.selectionChannelsLocked(selection), preferred) ||
		!p.channelSupportsLocked(account, preferred, selection) {
		return false
	}
	_, cooling := accountCooldown(account, ChannelCooldownScope(preferred, selectionAccessScope(selection)), now)
	return !cooling
}

// channelSupportsLocked determines whether account channel catalog supports selection
func (p *AccountPool) channelSupportsLocked(account *Account, channel Channel, selection AccountSelection) bool {
	if strings.TrimSpace(selection.ModelID) == "" {
		return channel == ChannelPlayground
	}
	switch channel {
	case ChannelPlayground:
		if generationChannelSelection(selection) && !p.channelEnabledLocked(ChannelPlayground) {
			return false
		}
		return accountSupportsSelection(account, selection)
	case ChannelBuild:
		if buildMethodSelection(selection) {
			return p.channelEnabledLocked(ChannelBuild) && p.buildSupportsMethodLocked(account, selection.ModelID, selection.Method)
		}
		return generationChannelSelection(selection) && p.channelEnabledLocked(ChannelBuild) &&
			p.buildSupportsModelLocked(account, selection.ModelID)
	default:
		return false
	}
}

// accountSupportsAnyChannelLocked determines whether at least one channel in account supports selection
func (p *AccountPool) accountSupportsAnyChannelLocked(account *Account, selection AccountSelection) bool {
	for _, channel := range p.selectionChannelsLocked(selection) {
		if p.channelSupportsLocked(account, channel, selection) {
			return true
		}
	}
	return false
}

// accountChannelCooldownLocked returns earliest recovery time when all supported channels of account are cooling down
func (p *AccountPool) accountChannelCooldownLocked(account *Account, selection AccountSelection, now time.Time) (time.Time, bool) {
	scope := selectionAccessScope(selection)
	var earliest time.Time
	for _, channel := range p.selectionChannelsLocked(selection) {
		if !p.channelSupportsLocked(account, channel, selection) {
			continue
		}
		cooldown, active := accountCooldown(account, ChannelCooldownScope(channel, scope), now)
		if !active {
			return time.Time{}, false
		}
		if earliest.IsZero() || cooldown.Until.Before(earliest) {
			earliest = cooldown.Until
		}
	}
	return earliest, !earliest.IsZero()
}

// channelCandidatesLocked expands candidates by account ID and channel order; round-robin starts after last selected combination
func (p *AccountPool) channelCandidatesLocked(indices []int, selection AccountSelection) []channelCandidate {
	channels := p.selectionChannelsLocked(selection)
	candidates := make([]channelCandidate, 0, len(indices)*len(channels))
	for _, index := range indices {
		for _, channel := range channels {
			candidates = append(candidates, channelCandidate{index: index, channel: channel})
		}
	}
	rank := func(channel Channel) int { return slices.Index(channels, channel) }
	sort.SliceStable(candidates, func(left, right int) bool {
		leftID, rightID := p.accounts[candidates[left].index].ID, p.accounts[candidates[right].index].ID
		if leftID != rightID {
			return leftID < rightID
		}
		return rank(candidates[left].channel) < rank(candidates[right].channel)
	})
	if p.routingStrategy != "round-robin" || len(indices) <= 1 && len(channels) <= 1 {
		return candidates
	}
	key := selectionAccessScope(selection)
	lastAccount := p.lastPicked[key]
	lastChannel := p.lastPickedChannel[key]
	start := sort.Search(len(candidates), func(position int) bool {
		account := p.accounts[candidates[position].index].ID
		if account != lastAccount {
			return account > lastAccount
		}
		return lastChannel != "" && rank(candidates[position].channel) > rank(lastChannel)
	})
	return append(candidates[start:], candidates[:start]...)
}

// buildSupportsModelLocked determines whether account Build catalog can invoke model via generation request
func (p *AccountPool) buildSupportsModelLocked(account *Account, modelID string) bool {
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), "models/")
	found := false
	for _, model := range account.buildModels {
		if model.ID == modelID && hasMethod(model, "generateContent") {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	for _, candidate := range p.accounts {
		if candidate == nil {
			continue
		}
		for _, model := range candidate.Models {
			if modelMatchesID(model, modelID) {
				return !buildExcludedPlaygroundModel(model) && modelAllowedByTier(model, account.BenefitTier)
			}
		}
	}
	return !buildRequiresSpecialTool(modelID)
}

// buildExcludedPlaygroundModel determines whether Playground catalog model uses dedicated route not integrated into Build
func buildExcludedPlaygroundModel(model Model) bool {
	return model.Capabilities["interactions_api"] || model.Capabilities["interaction_route"] ||
		model.Capabilities["transcription_output"]
}

// buildRequiresSpecialTool determines whether Build-exclusive model requires special tools such as Computer Use
func buildRequiresSpecialTool(modelID string) bool {
	return strings.Contains(modelID, "computer-use")
}

// buildSupportsMethodLocked checks if account Build catalog can call a model absent from Playground catalog via embedding or realtime methods.
func (p *AccountPool) buildSupportsMethodLocked(account *Account, modelID string, method string) bool {
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), "models/")
	if p.hasPlaygroundModelLocked(modelID) {
		return false
	}
	for _, model := range account.buildModels {
		if model.ID == modelID && hasMethod(model, method) {
			return true
		}
	}
	return false
}

// buildPublicMethods returns callable methods in public catalog for Build-exclusive embedding and realtime models.
func buildPublicMethods(model Model) []string {
	var methods []string
	for _, method := range buildMethods {
		if !hasMethod(model, method) {
			continue
		}
		if method == "embedContent" {
			methods = append(methods, "batchEmbedContents")
		}
		methods = append(methods, method)
	}
	return methods
}

// buildOnlyModelsLocked returns generatable, embedding, and realtime models in enabled accounts' Build catalog absent from Playground catalog.
func (p *AccountPool) buildOnlyModelsLocked() []Model {
	if !p.channelEnabledLocked(ChannelBuild) {
		return nil
	}
	var models []Model
	seen := make(map[string]struct{})
	for _, account := range p.accounts {
		if account == nil || !account.Config.Enabled {
			continue
		}
		for _, model := range account.buildModels {
			if _, exists := seen[model.ID]; exists || p.hasPlaygroundModelLocked(model.ID) {
				continue
			}
			listed := cloneAccountModels([]Model{model})[0]
			if !hasMethod(model, "generateContent") {
				listed.Methods = buildPublicMethods(model)
			} else if !p.buildSupportsModelLocked(account, model.ID) {
				continue
			}
			if len(listed.Methods) == 0 {
				continue
			}
			seen[model.ID] = struct{}{}
			models = append(models, listed)
		}
	}
	return models
}

func (p *AccountPool) hasPlaygroundModelLocked(modelID string) bool {
	for _, account := range p.accounts {
		if account == nil {
			continue
		}
		for _, model := range account.Models {
			if modelMatchesID(model, modelID) {
				return true
			}
		}
	}
	return false
}

// BidiMethod returns model realtime method: bidiGenerateMusic for Build realtime music models, otherwise bidiGenerateContent
func (p *AccountPool) BidiMethod(modelID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hasBuildModelLocked(strings.TrimPrefix(strings.TrimSpace(modelID), "models/"), "bidiGenerateMusic") {
		return "bidiGenerateMusic"
	}
	return "bidiGenerateContent"
}

func (p *AccountPool) hasBuildModelLocked(modelID string, method string) bool {
	if !p.channelEnabledLocked(ChannelBuild) {
		return false
	}
	for _, account := range p.accounts {
		if account == nil {
			continue
		}
		for _, model := range account.buildModels {
			if model.ID != modelID || method != "" && !hasMethod(model, method) {
				continue
			}
			if hasMethod(model, "generateContent") && p.buildSupportsModelLocked(account, modelID) {
				return true
			}
			for _, served := range buildMethods {
				if (method == "" || method == served) && p.buildSupportsMethodLocked(account, modelID, served) {
					return true
				}
			}
		}
	}
	return false
}

// modelChannelsLocked returns channels through which at least one enabled account can invoke the model
func (p *AccountPool) modelChannelsLocked(model Model) []string {
	selection := AccountSelection{ModelID: model.ID}
	if hasMethod(model, "generateContent") {
		selection.Method = "generateContent"
	} else if index := slices.IndexFunc(buildMethods, func(method string) bool { return hasMethod(model, method) }); index >= 0 {
		selection.Method = buildMethods[index]
	}
	var channels []string
	for _, channel := range p.selectionChannelsLocked(selection) {
		for _, account := range p.accounts {
			if account != nil && account.Config.Enabled && p.channelSupportsLocked(account, channel, selection) {
				channels = append(channels, string(channel))
				break
			}
		}
	}
	return channels
}

// AccountChannelAvailable returns whether account has supported channels that are not cooling down for selection
func (p *AccountPool) AccountChannelAvailable(accountID string, selection AccountSelection) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.byID[strings.TrimSpace(accountID)]
	if account == nil || !account.Config.Enabled || !p.accountSupportsAnyChannelLocked(account, selection) {
		return false
	}
	_, cooling := p.accountChannelCooldownLocked(account, selection, time.Now())
	return !cooling
}
