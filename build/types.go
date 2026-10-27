// Package build compiles explicit Go package selections through a trusted application adapter.
// It owns resolution, isolated projects, input verification and delivery; applications own assembly.
package build

import "time"

// Request declares package selections separately from module requirements and
// source replacements. Dir and Output are explicit; relative paths use Dir.
type Request struct {
	ModulePath   string              `json:"modulePath"`
	Dir          string              `json:"dir"`
	Output       string              `json:"output"`
	Packages     []Package           `json:"packages"`
	Modules      []ModuleRequirement `json:"modules"`
	Replacements []Replacement       `json:"replacements"`
}

// Query preserves whether a version query was explicitly requested. Value must
// be nonempty even for implicit defaults, for example "latest".
type Query struct {
	Value    string `json:"value"`
	Explicit bool   `json:"explicit"`
}

// Package selects an importable package for application assembly.
type Package struct {
	Path   string  `json:"path"`
	Query  Query   `json:"query"`
	Source *Source `json:"source,omitempty"`
}

// ModuleRequirement adds a version requirement without selecting a package.
// Exact requires the final graph version to equal the resolved require version.
type ModuleRequirement struct {
	Path  string `json:"path"`
	Query Query  `json:"query"`
	Exact bool   `json:"exact"`
}

// Source contains either a local Directory or a remote Module and Query.
type Source struct {
	Directory string `json:"directory,omitempty"`
	Module    string `json:"module,omitempty"`
	Query     string `json:"query,omitempty"`
}

// Replacement maps a module, optionally at a fixed Version, to explicit source.
type Replacement struct {
	Module  string `json:"module"`
	Version string `json:"version,omitempty"`
	Source  Source `json:"source"`
}

// Adapter supplies trusted pure generation and optional record encoding, plus
// a candidate validation command. Generate is required; nil Record selects the
// default JSON encoder.
//
// Callbacks receive detached snapshots, must not change inputs, and cannot be
// forcibly interrupted; Build checks cancellation before and after each call.
type Adapter struct {
	Generate func(Plan) (Project, error)
	Validate Validation
	Record   func(Facts) (map[string][]byte, error)
}

// Project contains relative generated file names and contents, excluding module
// files and library-owned artifact paths. Root Go main code is required.
type Project struct {
	Files map[string][]byte
}

// Validation runs the candidate with Args, without a shell. Args must be
// nonempty. Zero Timeout means 30 seconds, bounded by the Build context.
type Validation struct {
	Args    []string
	Timeout time.Duration
}

// Result identifies a successfully delivered binary and its record directory.
// Both paths are absolute; errors return a zero Result.
type Result struct {
	BinaryPath   string
	RecordPath   string
	BinarySHA256 string
}

// Command supplies CLI presentation, default output, and application policy.
// Prepare is required and returns the request and adapter for a single Build.
type Command struct {
	Name          string
	DefaultOutput string
	Help          string
	AcceptApp     bool
	Prepare       func(Arguments) (Request, Adapter, error)
}

// Arguments is the parsed CLI input to Prepare. App is kept separate from
// Request.Packages; Prepare decides its role. Args preserves the original argv.
type Arguments struct {
	Request     Request
	App         *Package
	Args        []string
	Positionals []string
	OutputSet   bool
}

// Plan contains resolved ownership, requirements and fixed replacements before
// generation. It is not the final module graph.
type Plan struct {
	ModulePath   string                `json:"modulePath"`
	GoVersion    string                `json:"goVersion"`
	Packages     []ResolvedPackage     `json:"packages"`
	Requirements []ResolvedRequirement `json:"requirements"`
	Replacements []ResolvedReplacement `json:"replacements"`
}

// ResolvedPackage identifies a selected package and its actual owning module.
type ResolvedPackage struct {
	Path            string `json:"path"`
	Module          string `json:"module"`
	RequiredVersion string `json:"requiredVersion"`
}

// ResolvedRequirement records the effective query and generated require version,
// which may differ from the final module graph under MVS.
type ResolvedRequirement struct {
	Path                   string `json:"path"`
	Query                  Query  `json:"query"`
	RequiredVersion        string `json:"requiredVersion"`
	QueryExecuted          bool   `json:"queryExecuted"`
	DevelopmentPlaceholder bool   `json:"developmentPlaceholder"`
	Exact                  bool   `json:"exact"`
}

// ResolvedReplacement fixes a replacement before all ownership/build probes use
// it. Local directories are absolute; remote request Query remains in Source.
type ResolvedReplacement struct {
	Module        string `json:"module"`
	Version       string `json:"version,omitempty"`
	Source        Source `json:"source"`
	TargetVersion string `json:"targetVersion,omitempty"`
	QueryExecuted bool   `json:"queryExecuted"`
}

// Facts is the post-validation snapshot passed to the record encoder. Request
// preserves input intent, Plan records resolution, and Modules is Go's final graph.
type Facts struct {
	Request          Request             `json:"request"`
	Plan             Plan                `json:"plan"`
	Modules          []ModuleRecord      `json:"modules"`
	Replacements     []ReplacementRecord `json:"replacements"`
	Inputs           []InputRecord       `json:"inputs"`
	Commands         []CommandRecord     `json:"commands"`
	Environment      map[string]string   `json:"environment"`
	GeneratedSHA256  map[string]string   `json:"generatedSHA256"`
	BinarySHA256     string              `json:"binarySHA256"`
	LocalDevelopment bool                `json:"localDevelopment"`
}

// ModuleRecord retains the field names returned by go list -m -json.
type ModuleRecord struct {
	Path      string        `json:"Path"`
	Version   string        `json:"Version,omitempty"`
	Main      bool          `json:"Main,omitempty"`
	Dir       string        `json:"Dir,omitempty"`
	GoMod     string        `json:"GoMod,omitempty"`
	Sum       string        `json:"Sum,omitempty"`
	GoModSum  string        `json:"GoModSum,omitempty"`
	GoVersion string        `json:"GoVersion,omitempty"`
	Replace   *ModuleRecord `json:"Replace,omitempty"`
}

// ReplacementRecord includes declared but unused replacements with Used=false.
type ReplacementRecord struct {
	ResolvedReplacement
	Used bool `json:"used"`
}

// InputRecord hashes actual local inputs and module files. Files maps module
// relative slash paths to SHA-256; SHA256 hashes compact JSON of that map.
type InputRecord struct {
	Module    string            `json:"module"`
	Directory string            `json:"directory"`
	Files     map[string]string `json:"files"`
	SHA256    string            `json:"sha256"`
}

// CommandRecord indexes the ordered command log. An unavailable process exit
// code is represented by -1; candidate timeouts and outer cancellation differ.
type CommandRecord struct {
	Stage    string   `json:"stage"`
	Args     []string `json:"args"`
	Log      string   `json:"log"`
	ExitCode int      `json:"exitCode"`
	TimedOut bool     `json:"timedOut"`
	Canceled bool     `json:"canceled"`
}

// Record is the default record.json envelope, with Facts flattened at the root.
type Record struct {
	APIVersion string `json:"apiVersion"`
	Facts
}
