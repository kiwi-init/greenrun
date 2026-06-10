package engine

import (
	"strings"

	"gopkg.in/yaml.v3"
)

func scalarNode(value string) yaml.Node {
	return yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

// mappingNode builds a one-key matrix mapping such as {node: [18, 20]}.
func mappingNode(key string, values ...string) yaml.Node {
	var node yaml.Node
	_ = yaml.Unmarshal([]byte(key+": ["+strings.Join(values, ", ")+"]"), &node)
	return *node.Content[0]
}
