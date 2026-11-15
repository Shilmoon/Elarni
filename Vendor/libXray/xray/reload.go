package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xtls/libxray/share"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
)

// Elarni: a change of per-app routing while connected used to stop the core and start it again,
// which cut every connection for about two seconds. Such a change only replaces routing rules
// and adds or removes the outbounds the rules name, which the running core can take as it is.

// ErrRestartNeeded says the running core cannot take the configuration without a restart: it
// differs in more than its routing rules and the outbounds that come and go with them.
var ErrRestartNeeded = errors.New("restart_needed")

// runningConfig is the configuration the running core was started with, or last reloaded to.
var runningConfig string

// routingChange is what a configuration changes against the running one, when the running core
// can take it: the outbounds to add and the tags to remove. The rules are replaced as a whole.
type routingChange struct {
	added   []string
	removed []string
}

// canonical is the value's JSON with object keys in order, so equal settings compare equal
// however they were written.
func canonical(raw json.RawMessage) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	text, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(text)
}

// routingDiff compares two configurations. Everything but the routing rules and the list of
// outbounds must be the same; an outbound both have must be the same, and so must the first,
// which takes what no rule matches.
func routingDiff(running, next string) (routingChange, error) {
	var change routingChange
	var before, after map[string]json.RawMessage
	if json.Unmarshal([]byte(running), &before) != nil || json.Unmarshal([]byte(next), &after) != nil {
		return change, ErrRestartNeeded
	}
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		// One side without the key compares as different.
		if key != "routing" && key != "outbounds" && canonical(before[key]) != canonical(after[key]) {
			return change, ErrRestartNeeded
		}
	}
	routingRest := func(raw json.RawMessage) (string, bool) {
		var routing map[string]json.RawMessage
		if len(raw) == 0 {
			return "{}", true
		}
		if json.Unmarshal(raw, &routing) != nil {
			return "", false
		}
		delete(routing, "rules")
		text, err := json.Marshal(routing)
		if err != nil {
			return "", false
		}
		return canonical(text), true
	}
	restBefore, okBefore := routingRest(before["routing"])
	restAfter, okAfter := routingRest(after["routing"])
	if !okBefore || !okAfter || restBefore != restAfter {
		return change, ErrRestartNeeded
	}
	outbounds := func(raw json.RawMessage) (tags []string, byTag map[string]string, ok bool) {
		var list []json.RawMessage
		if len(raw) > 0 && json.Unmarshal(raw, &list) != nil {
			return nil, nil, false
		}
		byTag = map[string]string{}
		for _, item := range list {
			var named struct {
				Tag string `json:"tag"`
			}
			// One without a tag cannot be told from another, nor removed.
			if json.Unmarshal(item, &named) != nil || named.Tag == "" {
				return nil, nil, false
			}
			if _, repeated := byTag[named.Tag]; repeated {
				return nil, nil, false
			}
			tags = append(tags, named.Tag)
			byTag[named.Tag] = canonical(item)
		}
		return tags, byTag, true
	}
	tagsBefore, settingsBefore, okBefore := outbounds(before["outbounds"])
	tagsAfter, settingsAfter, okAfter := outbounds(after["outbounds"])
	if !okBefore || !okAfter || len(tagsBefore) == 0 || len(tagsAfter) == 0 || tagsBefore[0] != tagsAfter[0] {
		return change, ErrRestartNeeded
	}
	for _, tag := range tagsAfter {
		settings, kept := settingsBefore[tag]
		if !kept {
			change.added = append(change.added, tag)
		} else if settings != settingsAfter[tag] {
			return change, ErrRestartNeeded
		}
	}
	for _, tag := range tagsBefore {
		if _, kept := settingsAfter[tag]; !kept {
			change.removed = append(change.removed, tag)
		}
	}
	return change, nil
}

// ReloadRouting gives the running core the routing rules of configJSON, with the outbounds they
// newly name, and takes away the outbounds it no longer has. Connections already open keep the
// route they were given; new ones follow the new rules. ErrRestartNeeded says the configuration
// differs in more than that, and nothing was changed.
func ReloadRouting(configJSON string) error {
	coreServerMu.Lock()
	defer coreServerMu.Unlock()
	if coreServer == nil || !coreServer.IsRunning() {
		return errors.New("xray is not running")
	}
	change, err := routingDiff(runningConfig, configJSON)
	if err != nil {
		return err
	}
	if err := share.ValidateClientFileAccess([]byte(configJSON)); err != nil {
		return fmt.Errorf("xray_config_invalid: %w", err)
	}
	config, err := core.LoadConfig("json", bytes.NewReader([]byte(configJSON)))
	if err != nil {
		return fmt.Errorf("xray_config_invalid: %w", err)
	}
	// The router's own configuration, as the core would start with it. Without one the new
	// configuration has no rules, which a running router cannot be told.
	var rules *router.Config
	for _, app := range config.App {
		if instance, err := app.GetInstance(); err == nil {
			if found, ok := instance.(*router.Config); ok {
				rules = found
			}
		}
	}
	routes, ok := coreServer.GetFeature(routing.RouterType()).(*router.Router)
	manager, managed := coreServer.GetFeature(outbound.ManagerType()).(outbound.Manager)
	if rules == nil || !ok || !managed {
		return ErrRestartNeeded
	}
	var added []string
	undo := func() {
		for _, tag := range added {
			_ = manager.RemoveHandler(context.Background(), tag)
		}
	}
	for _, tag := range change.added {
		for _, handler := range config.Outbound {
			if handler.Tag != tag {
				continue
			}
			if err := core.AddOutboundHandler(coreServer, handler); err != nil {
				undo()
				return fmt.Errorf("xray_reload_failed: %w", err)
			}
			added = append(added, tag)
		}
	}
	// Rules first, outbounds last: no rule may name an outbound that is gone.
	if err := routes.ReloadRules(rules, false); err != nil {
		undo()
		return fmt.Errorf("xray_reload_failed: %w", err)
	}
	for _, tag := range change.removed {
		// Its open connections end by themselves; closing the handler would cut them.
		_ = manager.RemoveHandler(context.Background(), tag)
	}
	runningConfig = configJSON
	tunnelRules, tunnelRest = perAppGate(configJSON)
	return nil
}
