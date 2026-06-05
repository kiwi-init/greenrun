package engine

import "gopkg.in/yaml.v3"

func scalarNode(value string) yaml.Node {
	return yaml.Node{Kind: yaml.ScalarNode, Value: value}
}
