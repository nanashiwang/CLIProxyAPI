package management

import (
	"reflect"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// preservePersonalV8Config applies the same identity and permission migration as
// the dedicated management editors without rewriting unrelated YAML settings.
func preservePersonalV8Config(root, before *yaml.Node) error {
	var next, previous config.Config
	if err := root.Decode(&next); err != nil {
		return err
	}
	if err := before.Decode(&previous); err != nil {
		return err
	}
	previous.EnsureOpenAICompatibilityIDs()
	config.PreserveOpenAICompatibilityIDs(next.OpenAICompatibility, previous.OpenAICompatibility)
	if err := next.ValidateOpenAICompatibilityIDs(); err != nil {
		return err
	}
	groups := configV8Node(root, []string{"api-keys", "openai-compatibility"})
	if groups != nil && groups.Kind == yaml.SequenceNode {
		for i, group := range groups.Content {
			if i >= len(next.OpenAICompatibility) {
				break
			}
			provider := next.OpenAICompatibility[i]
			setPersonalV8Node(group, "id", provider.ID)
			keys := configV8Node(group, []string{"keys"})
			if keys == nil || keys.Kind != yaml.SequenceNode {
				continue
			}
			for j, key := range keys.Content {
				if j < len(provider.APIKeyEntries) {
					setPersonalV8Node(key, "id", provider.APIKeyEntries[j].ID)
				}
			}
		}
	}
	// Explicitly editing pool rules remains supported. An ordinary key rotation
	// carries the unchanged permission assignment forward to its new fingerprint.
	if !reflect.DeepEqual(next.APIKeys, previous.APIKeys) && reflect.DeepEqual(next.AccountPools.KeyRules, previous.AccountPools.KeyRules) {
		rules, err := reconcilePoolKeyRules(previous.AccountPools.KeyRules, previous.APIKeys, next.APIKeys)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(rules, next.AccountPools.KeyRules) {
			pools := configV8Node(root, []string{"account-pools"})
			if pools != nil {
				setPersonalV8Node(pools, "key-rules", rules)
			}
		}
	}
	return nil
}

func setPersonalV8Node(parent *yaml.Node, key string, value any) {
	var node yaml.Node
	if node.Encode(value) != nil {
		return
	}
	if existing := configV8Node(parent, []string{key}); existing != nil {
		node.HeadComment, node.LineComment, node.FootComment = existing.HeadComment, existing.LineComment, existing.FootComment
		*existing = node
		return
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &node)
}
