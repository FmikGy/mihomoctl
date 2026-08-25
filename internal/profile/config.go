package profile

import (
	"bytes"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

const defaultTestURL = "https://www.gstatic.com/generate_204"

// GenerateConfig builds a useful rule-mode Mihomo configuration from nodes.
func GenerateConfig(nodes []Node) ([]byte, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("at least one proxy node is required")
	}
	nodes = uniqueNodeNames(nodes)
	proxies := make([]map[string]any, 0, len(nodes))
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Type == "" || node.Name == "" {
			return nil, fmt.Errorf("node name and type are required")
		}
		proxy := make(map[string]any, len(node.Config)+2)
		for key, value := range node.Config {
			if key == "name" || key == "type" {
				continue
			}
			proxy[key] = value
		}
		proxy["name"] = node.Name
		proxy["type"] = node.Type
		proxies = append(proxies, proxy)
		names = append(names, node.Name)
	}
	proxyChoices := append([]string{"AUTO"}, names...)
	proxyChoices = append(proxyChoices, "DIRECT")
	config := map[string]any{
		"mixed-port": 7890,
		"allow-lan":  false,
		"mode":       "rule",
		"log-level":  "info",
		"ipv6":       false,
		"dns":        defaultDNSConfig(false),
		"profile": map[string]any{
			"store-selected": true,
			"store-fake-ip":  true,
		},
		"geodata-mode": true,
		"geox-url": map[string]any{
			"geoip":   "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat",
			"geosite": "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
			"mmdb":    "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
			"asn":     "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb",
		},
		"proxies": proxies,
		"proxy-groups": []map[string]any{
			{"name": "PROXY", "type": "select", "proxies": proxyChoices},
			{"name": "AUTO", "type": "url-test", "proxies": names, "url": defaultTestURL, "interval": 300, "tolerance": 50},
		},
		"rules": []string{
			"GEOIP,LAN,DIRECT,no-resolve",
			"GEOSITE,CN,DIRECT",
			"GEOIP,CN,DIRECT,no-resolve",
			"MATCH,PROXY",
		},
	}
	result, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode generated config: %w", err)
	}
	return result, nil
}

// NormalizeSource validates Mihomo YAML or converts a URI subscription into
// Mihomo YAML. The boolean reports whether conversion occurred.
func NormalizeSource(source []byte) ([]byte, bool, error) {
	if nodes, err := ParseURIList(string(source)); err == nil {
		config, generateErr := GenerateConfig(nodes)
		return config, true, generateErr
	}
	doc, err := decodeYAMLDocument(source)
	if err != nil {
		return nil, false, fmt.Errorf("source is neither valid Mihomo YAML nor a URI subscription: %w", err)
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("Mihomo YAML root must be a mapping")
	}
	return append([]byte(nil), source...), false, nil
}

// MergeManagedConfig applies mihomoctl-owned settings with yaml.Node so keys,
// values, and comments outside the managed overlay survive unchanged.
func MergeManagedConfig(raw []byte, options OverlayOptions) ([]byte, error) {
	doc, err := decodeYAMLDocument(raw)
	if err != nil {
		return nil, err
	}
	if err := ApplyManagedOverlay(doc, options); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode managed config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finish managed config: %w", err)
	}
	return output.Bytes(), nil
}

// ApplyManagedOverlay mutates a parsed YAML document in place.
func ApplyManagedOverlay(doc *yaml.Node, options OverlayOptions) error {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("Mihomo YAML root must be a mapping")
	}
	root := doc.Content[0]
	controller := options.ExternalController
	if controller == "" {
		controller = "127.0.0.1:9090"
	}
	setMapValue(root, "external-controller", scalarString(controller))
	setMapValue(root, "secret", scalarString(options.Secret))

	profile, err := ensureMapValue(root, "profile")
	if err != nil {
		return err
	}
	storeSelected := true
	if options.StoreSelected != nil {
		storeSelected = *options.StoreSelected
	}
	setMapValue(profile, "store-selected", scalarBool(storeSelected))

	settings := options.Settings
	if settings.Mode != "" {
		setMapValue(root, "mode", scalarString(string(settings.Mode)))
	}
	if settings.MixedPort != nil {
		setMapValue(root, "mixed-port", scalarInt(*settings.MixedPort))
	}
	if settings.AllowLAN != nil {
		setMapValue(root, "allow-lan", scalarBool(*settings.AllowLAN))
	}
	if settings.IPv6 != nil {
		setMapValue(root, "ipv6", scalarBool(*settings.IPv6))
	}
	if settings.LogLevel != "" {
		setMapValue(root, "log-level", scalarString(settings.LogLevel))
	}
	if settings.TUN && mapValue(root, "dns") == nil {
		dnsIPv6 := false
		if settings.IPv6 != nil {
			dnsIPv6 = *settings.IPv6
		}
		dns := &yaml.Node{}
		if err := dns.Encode(defaultDNSConfig(dnsIPv6)); err != nil {
			return fmt.Errorf("encode default DNS config: %w", err)
		}
		setMapValue(root, "dns", dns)
		setMapValueIfMissing(profile, "store-fake-ip", scalarBool(true))
	}
	if dns := mapValue(root, "dns"); shouldAddProxyServerNameserver(dns) {
		// Keep proxy endpoint lookup independent from subscription DNS. An
		// explicit proxy resolver in the source always takes precedence.
		setMapValueIfMissing(dns, "proxy-server-nameserver", &yaml.Node{
			Kind: yaml.SequenceNode,
			Tag:  "!!seq",
			Content: []*yaml.Node{
				scalarString("system"),
			},
		})
	}

	tun, err := ensureMapValue(root, "tun")
	if err != nil {
		return err
	}
	setMapValue(tun, "enable", scalarBool(settings.TUN))
	if settings.TUN {
		setMapValueIfMissing(tun, "stack", scalarString("mixed"))
		setMapValueIfMissing(tun, "auto-route", scalarBool(true))
		setMapValueIfMissing(tun, "auto-redirect", scalarBool(true))
		setMapValueIfMissing(tun, "auto-detect-interface", scalarBool(true))
		setMapValueIfMissing(tun, "dns-hijack", &yaml.Node{
			Kind: yaml.SequenceNode,
			Tag:  "!!seq",
			Content: []*yaml.Node{
				scalarString("any:53"),
				scalarString("tcp://any:53"),
			},
		})
	}
	return nil
}

func shouldAddProxyServerNameserver(dns *yaml.Node) bool {
	if dns == nil || dns.Kind != yaml.MappingNode {
		return false
	}
	var semantic map[string]any
	if err := dns.Decode(&semantic); err != nil {
		return false
	}
	enabled, ok := semantic["enable"].(bool)
	if !ok || !enabled {
		return false
	}
	_, configured := semantic["proxy-server-nameserver"]
	return !configured
}

func defaultDNSConfig(ipv6 bool) map[string]any {
	return map[string]any{
		"enable":        true,
		"ipv6":          ipv6,
		"enhanced-mode": "fake-ip",
		"fake-ip-range": "198.18.0.1/16",
		"fake-ip-filter": []string{
			"*.lan",
			"*.local",
		},
		"default-nameserver": []string{
			"223.5.5.5",
			"1.1.1.1",
		},
		"nameserver": []string{
			"https://dns.alidns.com/dns-query",
			"https://cloudflare-dns.com/dns-query",
		},
		"proxy-server-nameserver": []string{
			"https://dns.alidns.com/dns-query",
			"https://cloudflare-dns.com/dns-query",
		},
	}
}

func decodeYAMLDocument(raw []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("YAML document is empty")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}
	return &doc, nil
}

func ensureMapValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	value := mapValue(mapping, key)
	if value == nil {
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapping.Content = append(mapping.Content, scalarString(key), value)
		return value, nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		comments := [3]string{value.HeadComment, value.LineComment, value.FootComment}
		*value = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: comments[0], LineComment: comments[1], FootComment: comments[2]}
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("managed key %q must be a mapping", key)
	}
	return value, nil
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setMapValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			old := mapping.Content[index+1]
			value.HeadComment = old.HeadComment
			value.LineComment = old.LineComment
			value.FootComment = old.FootComment
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalarString(key), value)
}

func setMapValueIfMissing(mapping *yaml.Node, key string, value *yaml.Node) {
	if mapValue(mapping, key) == nil {
		mapping.Content = append(mapping.Content, scalarString(key), value)
	}
}

func scalarString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func scalarBool(value bool) *yaml.Node {
	text := "false"
	if value {
		text = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text}
}

func scalarInt(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
}
