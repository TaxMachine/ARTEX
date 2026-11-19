// Canonical vulnerability/exploit-type taxonomy: the closed list every
// finding's vulnclass MUST come from. Free-text classes produced drift
// ("denial-of-service" vs "dos" vs "logic/denial-of-service (VBA parser)").
// Curated from CAPEC views + CWE classes + practical pentest groupings.
package db

// VulnClass is one canonical exploit type.
type VulnClass struct {
	ID   string `json:"id"`   // slug stored in findings.vulnclass
	Name string `json:"name"` // human label
}

// VulnClasses is the full static list. Grouped by family via id prefixes.
var VulnClasses = []VulnClass{
	// --- memory corruption ---
	{"memory-oob-read", "Out-of-bounds read"},
	{"memory-oob-write", "Out-of-bounds write"},
	{"memory-use-after-free", "Use-after-free"},
	{"memory-double-free", "Double free"},
	{"memory-integer-overflow", "Integer overflow/wraparound"},
	{"memory-integer-underflow", "Integer underflow"},
	{"memory-type-confusion", "Type confusion"},
	{"memory-uninitialized-use", "Uninitialized memory use"},
	{"memory-null-deref", "Null pointer dereference"},
	{"memory-stack-overflow", "Stack buffer overflow"},
	{"memory-heap-overflow", "Heap buffer overflow"},
	{"memory-format-string", "Format string"},

	// --- code execution / injection ---
	{"rce-memory-corruption", "RCE via memory corruption"},
	{"rce-deserialization", "RCE via unsafe deserialization"},
	{"rce-command-injection", "RCE via command injection"},
	{"rce-code-injection", "RCE via code evaluation/injection"},
	{"rce-file-upload", "RCE via file upload"},
	{"rce-logic", "RCE via logic flaw"},
	{"rce-sandbox-escape", "Sandbox / container escape"},
	{"sql-injection", "SQL injection"},
	{"nosql-injection", "NoSQL injection"},
	{"xpath-injection", "XPath injection"},
	{"ldap-injection", "LDAP injection"},
	{"template-injection", "Template injection (SSTI)"},
	{"script-injection", "Script/interpreter injection (XSS to RCE path)"},

	// --- access control / auth ---
	{"auth-bypass", "Authentication bypass"},
	{"auth-weak-credentials", "Weak/default credentials"},
	{"auth-session-fixation", "Session fixation"},
	{"auth-token-reuse", "Token/nonce reuse"},
	{"access-broken-object-level", "Broken object level authorization (IDOR)"},
	{"access-broken-function-level", "Broken function level authorization"},
	{"access-privilege-escalation", "Privilege escalation"},
	{"access-privilege-escalation-kernel", "Kernel privilege escalation"},
	{"access-privilege-escalation-service", "Service account privilege escalation"},
	{"access-missing-authorization", "Missing authorization check"},
	{"access-path-traversal", "Path traversal / arbitrary file read"},

	// --- injection-adjacent / data handling ---
	{"xxe", "XML external entity (XXE)"},
	{"ssrf", "Server-side request forgery"},
	{"csrf", "Cross-site request forgery"},
	{"xss", "Cross-site scripting"},
	{"open-redirect", "Open redirect"},
	{"request-smuggling", "HTTP request smuggling"},
	{"request-forgery", "Request forgery / header injection"},

	// --- information exposure ---
	{"info-disclosure", "Information disclosure"},
	{"info-credential-leak", "Credential disclosure"},
	{"info-sensitive-data", "Sensitive data exposure"},
	{"info-error-leak", "Verbose error / stack leak"},
	{"info-source-disclosure", "Source code disclosure"},
	{"info-metadata-leak", "Metadata / backup file leak"},

	// --- crypto ---
	{"crypto-weak-algorithm", "Weak cryptographic algorithm"},
	{"crypto-weak-randomness", "Insufficient randomness"},
	{"crypto-hardcoded-keys", "Hardcoded cryptographic key"},
	{"crypto-weak-hash", "Weak hashing (unsalted/reversible)"},
	{"crypto-certificate-validation", "Improper certificate validation"},

	// --- availability ---
	{"dos-memory", "Denial of service (memory exhaustion/corruption)"},
	{"dos-cpu", "Denial of service (CPU exhaustion)"},
	{"dos-crash", "Denial of service (crash/terminate)"},
	{"dos-slow-loris", "Denial of service (slow request)"},
	{"race-condition", "Race condition"},
	{"logic-flaw", "Business logic flaw"},
	{"logic-insecure-design", "Insecure design"},

	// --- configuration / supply chain ---
	{"config-default-credentials", "Default credentials in config"},
	{"config-misconfiguration", "Security misconfiguration"},
	{"config-permissive-cors", "Permissive CORS"},
	{"config-verbose-logging", "Sensitive data in logs"},
	{"supply-chain-dependency", "Vulnerable dependency"},
	{"supply-chain-update-hijack", "Update/dependency hijacking"},

	// --- platform specific ---
	{"driver-ioctl-validation", "Driver IOCTL input validation flaw"},
	{"driver-pool-corruption", "Driver pool corruption"},
	{"kernel-info-leak", "Kernel information disclosure"},
	{"hypervisor-escape", "Hypervisor escape"},
	{"fileformat-parser-flaw", "File-format parser flaw"},
	{"protocol-flaw", "Protocol implementation flaw"},
	{"api-abuse", "API abuse / missing rate limit"},
	{"physical", "Physical access"},
	{"social-engineering", "Social engineering"},
	{"other", "Other / unclassified"},
}

var vulnClassByID = func() map[string]VulnClass {
	m := make(map[string]VulnClass, len(VulnClasses))
	for _, c := range VulnClasses {
		m[c.ID] = c
	}
	return m
}()

// NormalizeVulnClass maps free-text input to a canonical id: exact/near slug
// matches, common synonyms, and known-bad historical values. Returns ("",false)
// when nothing recognizable — the caller decides reject vs other.
func NormalizeVulnClass(s string) (string, bool) {
	slug := NormalizeSlug(s)
	if _, ok := vulnClassByID[slug]; ok {
		return slug, true
	}
	// historical/synonym map (normalized on both sides)
	syn := map[string]string{
		"dos": "dos-crash", "denial-of-service": "dos-crash", "denial of service": "dos-crash",
		"rce": "rce-memory-corruption", "remote-code-execution": "rce-memory-corruption",
		"remote code execution": "rce-memory-corruption", "code-execution": "rce-memory-corruption",
		"lpe": "access-privilege-escalation", "local-privilege-escalation": "access-privilege-escalation",
		"privilege-escalation": "access-privilege-escalation", "privesc": "access-privilege-escalation",
		"idor": "access-broken-object-level", "insecure-direct-object-reference": "access-broken-object-level",
		"broken-access-control": "access-missing-authorization", "improper-authorization": "access-missing-authorization",
		"broken-authentication": "auth-bypass", "auth": "auth-bypass",
		"info-leak": "info-disclosure", "information-disclosure": "info-disclosure",
		"credential-disclosure": "info-credential-leak", "credentials-leak": "info-credential-leak",
		"integer-overflow": "memory-integer-overflow", "integer_overflow": "memory-integer-overflow",
		"integer-underflow": "memory-integer-underflow", "integer_underflow": "memory-integer-underflow",
		"kernel-oob-write": "memory-oob-write", "oob-write": "memory-oob-write", "out-of-bounds-write": "memory-oob-write",
		"oob-read": "memory-oob-read", "out-of-bounds-read": "memory-oob-read",
		"uaf": "memory-use-after-free", "use-after-free": "memory-use-after-free",
		"heap-overflow": "memory-heap-overflow", "stack-overflow": "memory-stack-overflow",
		"buffer-overflow": "memory-heap-overflow", "format-string": "memory-format-string",
		"type-confusion": "memory-type-confusion", "null-deref": "memory-null-deref",
		"command-injection": "rce-command-injection", "command exec": "rce-command-injection",
		"deserialization": "rce-deserialization", "insecure-deserialization": "rce-deserialization",
		"file-upload": "rce-file-upload", "arbitrary-file-upload": "rce-file-upload",
		"ssrf": "ssrf", "xxe": "xxe", "csrf": "csrf", "xss": "xss",
		"sql-injection": "sql-injection", "sqli": "sql-injection",
		"path-traversal": "access-path-traversal", "directory-traversal": "access-path-traversal",
		"logic": "logic-flaw", "business-logic": "logic-flaw",
		"race": "race-condition", "race-condition": "race-condition", "tor": "race-condition",
		"open-redirect": "open-redirect", "redirect": "open-redirect",
		"cors": "config-permissive-cors", "misconfiguration": "config-misconfiguration",
		"weak-crypto": "crypto-weak-algorithm", "weak-encryption": "crypto-weak-algorithm",
		"hardcoded-key": "crypto-hardcoded-keys",
	}
	if v, ok := syn[slug]; ok {
		return v, true
	}
	return "", false
}

// NormalizeSlug lowercases, trims, and reduces to [a-z0-9-] (spaces/underscores
// to dash, parentheses content dropped).
func NormalizeSlug(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		case r >= '0' && r <= '9':
			out = append(out, r)
		case r == ' ', r == '_', r == '/', r == '.', r == ':':
			out = append(out, '-')
		case r == '(':
			return string(out) // drop "(verbose suffixes)"
		}
	}
	// trim dashes
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}
