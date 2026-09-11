package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

var supportedSchemes = map[string]bool{
	"ss": true, "vmess": true, "vless": true, "trojan": true,
	"hysteria2": true, "hy2": true, "tuic": true,
}

// ParseURI parses one supported proxy URI into a normalized Mihomo node.
func ParseURI(raw string) (Node, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 {
		return Node{}, fmt.Errorf("proxy URI has no scheme")
	}
	scheme := strings.ToLower(raw[:colon])
	if !supportedSchemes[scheme] {
		return Node{}, fmt.Errorf("unsupported proxy scheme %q", scheme)
	}

	var (
		node Node
		err  error
	)
	switch scheme {
	case "ss":
		node, err = parseSS(raw)
	case "vmess":
		node, err = parseVMess(raw)
	case "vless":
		node, err = parseVLESS(raw)
	case "trojan":
		node, err = parseTrojan(raw)
	case "hysteria2", "hy2":
		node, err = parseHysteria2(raw)
	case "tuic":
		node, err = parseTUIC(raw)
	}
	if err != nil {
		return Node{}, fmt.Errorf("parse %s URI: %w", scheme, err)
	}
	return node, nil
}

// ParseURIList accepts one URI, one URI per line, or a base64-encoded list.
func ParseURIList(input string) ([]Node, error) {
	return parseURIList(input, true)
}

func parseURIList(input string, allowBase64 bool) ([]Node, error) {
	input = strings.TrimSpace(strings.TrimPrefix(input, "\ufeff"))
	if input == "" {
		return nil, fmt.Errorf("URI subscription is empty")
	}
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) > 0 {
		allURIs := true
		for _, line := range filtered {
			colon := strings.IndexByte(line, ':')
			if colon <= 0 || !supportedSchemes[strings.ToLower(line[:colon])] {
				allURIs = false
				break
			}
		}
		if allURIs {
			nodes := make([]Node, 0, len(filtered))
			for index, line := range filtered {
				node, err := ParseURI(line)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", index+1, err)
				}
				nodes = append(nodes, node)
			}
			return uniqueNodeNames(nodes), nil
		}
	}
	if allowBase64 {
		decoded, err := decodeBase64String(input)
		if err == nil {
			return parseURIList(string(decoded), false)
		}
	}
	return nil, fmt.Errorf("source is neither a supported URI list nor a base64 URI subscription")
}

func uniqueNodeNames(nodes []Node) []Node {
	used := map[string]int{"PROXY": 1, "AUTO": 1, "DIRECT": 1, "REJECT": 1}
	result := make([]Node, len(nodes))
	for i, node := range nodes {
		base := strings.TrimSpace(node.Name)
		if base == "" {
			base = strings.ToUpper(node.Type)
		}
		name := base
		if used[name] > 0 {
			for suffix := used[base] + 1; ; suffix++ {
				candidate := fmt.Sprintf("%s (%d)", base, suffix)
				if used[candidate] == 0 {
					name = candidate
					used[base] = suffix
					break
				}
			}
		}
		used[name]++
		node.Name = name
		result[i] = node
	}
	return result
}

func parseSS(raw string) (Node, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return Node{}, err
	}
	name := fragmentName(u, "Shadowsocks")
	query := u.Query()
	if err := rejectUnknown(query, "plugin", "udp", "tfo", "servername"); err != nil {
		return Node{}, err
	}

	var method, password, host string
	var port int
	if u.User != nil && u.Hostname() != "" {
		host, port, err = parseHostPort(u)
		if err != nil {
			return Node{}, err
		}
		if directPassword, ok := u.User.Password(); ok {
			method = u.User.Username()
			password = directPassword
		} else {
			decoded, decodeErr := decodeBase64String(u.User.Username())
			if decodeErr != nil {
				return Node{}, fmt.Errorf("credentials are not method:password or base64: %w", decodeErr)
			}
			method, password, err = splitCredentials(string(decoded))
			if err != nil {
				return Node{}, err
			}
		}
	} else {
		payload := strings.TrimPrefix(raw, raw[:strings.IndexByte(raw, ':')+1])
		payload = strings.TrimPrefix(payload, "//")
		if index := strings.IndexAny(payload, "?#"); index >= 0 {
			payload = payload[:index]
		}
		payload, err = url.QueryUnescape(payload)
		if err != nil {
			return Node{}, fmt.Errorf("decode legacy payload: %w", err)
		}
		decoded, decodeErr := decodeBase64String(payload)
		if decodeErr != nil {
			return Node{}, fmt.Errorf("decode legacy payload: %w", decodeErr)
		}
		at := strings.LastIndexByte(string(decoded), '@')
		if at <= 0 {
			return Node{}, fmt.Errorf("legacy payload has no server")
		}
		method, password, err = splitCredentials(string(decoded[:at]))
		if err != nil {
			return Node{}, err
		}
		host, port, err = splitHostPort(string(decoded[at+1:]))
		if err != nil {
			return Node{}, err
		}
	}
	if method == "" || password == "" {
		return Node{}, fmt.Errorf("cipher and password are required")
	}
	config := map[string]any{
		"server": host, "port": port, "cipher": method, "password": password, "udp": true,
	}
	if value := query.Get("udp"); value != "" {
		config["udp"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	if value := query.Get("tfo"); value != "" {
		config["tfo"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	if value := query.Get("servername"); value != "" {
		config["servername"] = value
	}
	if value := query.Get("plugin"); value != "" {
		if err := applySSPlugin(config, value); err != nil {
			return Node{}, err
		}
	}
	return Node{Name: name, Type: "ss", Config: config}, nil
}

func splitCredentials(value string) (string, string, error) {
	method, password, ok := strings.Cut(value, ":")
	if !ok || method == "" || password == "" {
		return "", "", fmt.Errorf("credentials must be method:password")
	}
	return method, password, nil
}

func applySSPlugin(config map[string]any, value string) error {
	parts := strings.Split(value, ";")
	name := strings.ToLower(parts[0])
	opts := make(map[string]any)
	flags := make(map[string]bool)
	for _, part := range parts[1:] {
		if key, val, ok := strings.Cut(part, "="); ok {
			opts[key] = val
		} else if part != "" {
			flags[part] = true
		}
	}
	switch name {
	case "obfs-local", "simple-obfs", "obfs":
		config["plugin"] = "obfs"
		pluginOpts := make(map[string]any)
		if mode, ok := opts["obfs"]; ok {
			pluginOpts["mode"] = mode
		}
		if host, ok := opts["obfs-host"]; ok {
			pluginOpts["host"] = host
		}
		config["plugin-opts"] = pluginOpts
	case "v2ray-plugin":
		config["plugin"] = "v2ray-plugin"
		pluginOpts := make(map[string]any)
		for _, key := range []string{"mode", "host", "path"} {
			if val, ok := opts[key]; ok {
				pluginOpts[key] = val
			}
		}
		if flags["tls"] {
			pluginOpts["tls"] = true
		}
		if flags["mux"] {
			pluginOpts["mux"] = true
		}
		config["plugin-opts"] = pluginOpts
	default:
		return fmt.Errorf("unsupported Shadowsocks plugin %q", parts[0])
	}
	return nil
}

func parseVMess(raw string) (Node, error) {
	payload := strings.TrimSpace(raw[len("vmess:"):])
	payload = strings.TrimPrefix(payload, "//")
	decoded, err := decodeBase64String(payload)
	if err != nil {
		return Node{}, fmt.Errorf("decode JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return Node{}, fmt.Errorf("decode JSON: %w", err)
	}
	server := jsonString(data["add"])
	uuid := jsonString(data["id"])
	port, err := jsonInt(data["port"])
	if err != nil || server == "" || uuid == "" || port < 1 || port > 65535 {
		return Node{}, fmt.Errorf("add, port, and id are required")
	}
	alterID, _ := jsonInt(data["aid"])
	cipher := jsonString(data["scy"])
	if cipher == "" {
		cipher = "auto"
	}
	network := strings.ToLower(jsonString(data["net"]))
	if network == "" {
		network = "tcp"
	}
	config := map[string]any{
		"server": server, "port": port, "uuid": uuid, "alterId": alterID,
		"cipher": cipher, "udp": true, "network": network,
	}
	if err := applyVMessTransport(config, network, data); err != nil {
		return Node{}, err
	}
	if tls := strings.ToLower(jsonString(data["tls"])); tls != "" && tls != "none" {
		if tls != "tls" {
			return Node{}, fmt.Errorf("unsupported security %q", tls)
		}
		config["tls"] = true
	}
	if sni := jsonString(data["sni"]); sni != "" {
		config["servername"] = sni
	}
	if fp := jsonString(data["fp"]); fp != "" {
		config["client-fingerprint"] = fp
	}
	if alpn := splitCSV(jsonString(data["alpn"])); len(alpn) > 0 {
		config["alpn"] = alpn
	}
	if insecure, ok := jsonBool(data["allowInsecure"]); ok {
		config["skip-cert-verify"] = insecure
	}
	name := jsonString(data["ps"])
	if name == "" {
		name = server
	}
	return Node{Name: name, Type: "vmess", Config: config}, nil
}

func applyVMessTransport(config map[string]any, network string, data map[string]any) error {
	switch network {
	case "tcp":
		return nil
	case "ws":
		opts := map[string]any{}
		if path := jsonString(data["path"]); path != "" {
			opts["path"] = path
		}
		if host := jsonString(data["host"]); host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		config["ws-opts"] = opts
	case "grpc":
		service := jsonString(data["path"])
		if service == "" {
			service = jsonString(data["serviceName"])
		}
		config["grpc-opts"] = map[string]any{"grpc-service-name": service}
	case "h2":
		opts := map[string]any{}
		if path := jsonString(data["path"]); path != "" {
			opts["path"] = path
		}
		if hosts := splitCSV(jsonString(data["host"])); len(hosts) > 0 {
			opts["host"] = hosts
		}
		config["h2-opts"] = opts
	case "http":
		config["http-opts"] = map[string]any{"path": []string{jsonString(data["path"])}}
	default:
		return fmt.Errorf("unsupported transport %q", network)
	}
	return nil
}

func parseVLESS(raw string) (Node, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return Node{}, err
	}
	server, port, err := parseHostPort(u)
	if err != nil {
		return Node{}, err
	}
	if u.User == nil || u.User.Username() == "" {
		return Node{}, fmt.Errorf("UUID is required")
	}
	q := u.Query()
	if err := rejectUnknown(q, "encryption", "security", "sni", "servername", "type", "network", "host", "path", "flow", "fp", "pbk", "publickey", "sid", "shortid", "alpn", "servicename", "serviceName", "allowinsecure", "allowInsecure", "insecure", "udp", "packetencoding", "packetEncoding", "ed", "eh"); err != nil {
		return Node{}, err
	}
	if encryption := strings.ToLower(q.Get("encryption")); encryption != "" && encryption != "none" {
		return Node{}, fmt.Errorf("unsupported encryption %q", encryption)
	}
	config := map[string]any{"server": server, "port": port, "uuid": u.User.Username(), "udp": true}
	if flow := q.Get("flow"); flow != "" {
		config["flow"] = flow
	}
	if value := queryFirst(q, "udp"); value != "" {
		config["udp"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	if value := queryFirst(q, "packetEncoding", "packetencoding"); value != "" {
		config["packet-encoding"] = value
	}
	if err := applyURLSecurity(config, q, true); err != nil {
		return Node{}, err
	}
	if err := applyURLTransport(config, q); err != nil {
		return Node{}, err
	}
	return Node{Name: fragmentName(u, server), Type: "vless", Config: config}, nil
}

func parseTrojan(raw string) (Node, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return Node{}, err
	}
	server, port, err := parseHostPort(u)
	if err != nil {
		return Node{}, err
	}
	if u.User == nil || u.User.Username() == "" {
		return Node{}, fmt.Errorf("password is required")
	}
	q := u.Query()
	if err := rejectUnknown(q, "security", "sni", "servername", "type", "network", "host", "path", "fp", "alpn", "servicename", "serviceName", "allowinsecure", "allowInsecure", "insecure", "udp", "ed", "eh"); err != nil {
		return Node{}, err
	}
	config := map[string]any{"server": server, "port": port, "password": u.User.Username(), "udp": true}
	if value := queryFirst(q, "udp"); value != "" {
		config["udp"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	if err := applyURLSecurity(config, q, false); err != nil {
		return Node{}, err
	}
	if err := applyURLTransport(config, q); err != nil {
		return Node{}, err
	}
	return Node{Name: fragmentName(u, server), Type: "trojan", Config: config}, nil
}

func applyURLSecurity(config map[string]any, q url.Values, allowReality bool) error {
	security := strings.ToLower(q.Get("security"))
	if security == "" && !allowReality {
		security = "tls"
	}
	switch security {
	case "", "none":
	case "tls":
		config["tls"] = true
	case "reality":
		if !allowReality {
			return fmt.Errorf("reality is not supported by this protocol")
		}
		publicKey := queryFirst(q, "pbk", "publickey")
		if publicKey == "" {
			return fmt.Errorf("reality public key is required")
		}
		config["tls"] = true
		config["reality-opts"] = map[string]any{
			"public-key": publicKey,
			"short-id":   queryFirst(q, "sid", "shortid"),
		}
	default:
		return fmt.Errorf("unsupported security %q", security)
	}
	if sni := queryFirst(q, "sni", "servername"); sni != "" {
		config["servername"] = sni
	}
	if fp := q.Get("fp"); fp != "" {
		config["client-fingerprint"] = fp
	}
	if alpn := splitCSV(q.Get("alpn")); len(alpn) > 0 {
		config["alpn"] = alpn
	}
	if value := queryFirst(q, "allowInsecure", "allowinsecure", "insecure"); value != "" {
		insecure, err := boolQuery(value)
		if err != nil {
			return err
		}
		config["skip-cert-verify"] = insecure
	}
	return nil
}

func applyURLTransport(config map[string]any, q url.Values) error {
	network := strings.ToLower(queryFirst(q, "type", "network"))
	if network == "" {
		network = "tcp"
	}
	config["network"] = network
	switch network {
	case "tcp":
		return nil
	case "ws":
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if host := q.Get("host"); host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		if value := q.Get("ed"); value != "" {
			ed, err := strconv.Atoi(value)
			if err != nil || ed < 0 {
				return fmt.Errorf("invalid early data %q", value)
			}
			opts["max-early-data"] = ed
			opts["early-data-header-name"] = queryFirst(q, "eh")
		}
		config["ws-opts"] = opts
	case "grpc":
		config["grpc-opts"] = map[string]any{"grpc-service-name": queryFirst(q, "serviceName", "servicename")}
	case "http":
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = []string{path}
		}
		if hosts := splitCSV(q.Get("host")); len(hosts) > 0 {
			opts["headers"] = map[string]any{"Host": hosts}
		}
		config["http-opts"] = opts
	case "h2":
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if hosts := splitCSV(q.Get("host")); len(hosts) > 0 {
			opts["host"] = hosts
		}
		config["h2-opts"] = opts
	default:
		return fmt.Errorf("unsupported transport %q", network)
	}
	return nil
}

func parseHysteria2(raw string) (Node, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return Node{}, err
	}
	server, port, err := parseHostPort(u)
	if err != nil {
		return Node{}, err
	}
	if u.User == nil || u.User.Username() == "" {
		return Node{}, fmt.Errorf("password is required")
	}
	q := u.Query()
	if err := rejectUnknown(q, "sni", "peer", "insecure", "allowinsecure", "allowInsecure", "obfs", "obfs-password", "obfspassword", "up", "down", "alpn", "pinsha256", "fastopen", "ports", "hop-interval"); err != nil {
		return Node{}, err
	}
	password := u.User.Username()
	if extra, ok := u.User.Password(); ok {
		password += ":" + extra
	}
	config := map[string]any{"server": server, "port": port, "password": password}
	if sni := queryFirst(q, "sni", "peer"); sni != "" {
		config["sni"] = sni
	}
	if alpn := splitCSV(q.Get("alpn")); len(alpn) > 0 {
		config["alpn"] = alpn
	}
	if value := queryFirst(q, "insecure", "allowInsecure", "allowinsecure"); value != "" {
		config["skip-cert-verify"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	for _, pair := range [][2]string{{"obfs", "obfs"}, {"obfs-password", "obfs-password"}, {"obfspassword", "obfs-password"}, {"up", "up"}, {"down", "down"}, {"pinsha256", "pinSHA256"}, {"ports", "ports"}, {"hop-interval", "hop-interval"}} {
		if value := q.Get(pair[0]); value != "" {
			config[pair[1]] = value
		}
	}
	if value := q.Get("fastopen"); value != "" {
		config["fast-open"], err = boolQuery(value)
		if err != nil {
			return Node{}, err
		}
	}
	return Node{Name: fragmentName(u, server), Type: "hysteria2", Config: config}, nil
}

func parseTUIC(raw string) (Node, error) {
	u, err := parseProxyURL(raw)
	if err != nil {
		return Node{}, err
	}
	server, port, err := parseHostPort(u)
	if err != nil {
		return Node{}, err
	}
	if u.User == nil || u.User.Username() == "" {
		return Node{}, fmt.Errorf("UUID is required")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return Node{}, fmt.Errorf("password is required")
	}
	q := u.Query()
	if err := rejectUnknown(q, "sni", "alpn", "congestion_control", "congestion-controller", "udp_relay_mode", "udp-relay-mode", "allow_insecure", "insecure", "reduce_rtt", "disable_sni", "heartbeat_interval"); err != nil {
		return Node{}, err
	}
	config := map[string]any{"server": server, "port": port, "uuid": u.User.Username(), "password": password}
	if value := q.Get("sni"); value != "" {
		config["sni"] = value
	}
	if value := splitCSV(q.Get("alpn")); len(value) > 0 {
		config["alpn"] = value
	}
	for _, pair := range [][2]string{{"congestion_control", "congestion-controller"}, {"congestion-controller", "congestion-controller"}, {"udp_relay_mode", "udp-relay-mode"}, {"udp-relay-mode", "udp-relay-mode"}, {"heartbeat_interval", "heartbeat-interval"}} {
		if value := q.Get(pair[0]); value != "" {
			config[pair[1]] = value
		}
	}
	for _, pair := range [][2]string{{"allow_insecure", "skip-cert-verify"}, {"insecure", "skip-cert-verify"}, {"reduce_rtt", "reduce-rtt"}, {"disable_sni", "disable-sni"}} {
		if value := q.Get(pair[0]); value != "" {
			config[pair[1]], err = boolQuery(value)
			if err != nil {
				return Node{}, err
			}
		}
	}
	return Node{Name: fragmentName(u, server), Type: "tuic", Config: config}, nil
}

func rejectUnknown(values url.Values, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[strings.ToLower(key)] = true
	}
	for key := range values {
		if !set[strings.ToLower(key)] {
			return fmt.Errorf("unsupported parameter %q", key)
		}
	}
	return nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		// url.Error includes the full URI, which contains proxy credentials.
		return nil, fmt.Errorf("invalid URI syntax")
	}
	return parsed, nil
}

func queryFirst(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := values.Get(key); value != "" {
			return value
		}
		for existing, items := range values {
			if strings.EqualFold(existing, key) && len(items) > 0 && items[0] != "" {
				return items[0]
			}
		}
	}
	return ""
}

func jsonString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return ""
	}
}

func jsonInt(value any) (int, error) {
	text := jsonString(value)
	if text == "" {
		return 0, nil
	}
	result, err := strconv.Atoi(text)
	if err != nil {
		return 0, err
	}
	return result, nil
}

func jsonBool(value any) (bool, bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case string:
		result, err := boolQuery(value)
		return result, err == nil
	case json.Number:
		result, err := boolQuery(value.String())
		return result, err == nil
	default:
		return false, false
	}
}
