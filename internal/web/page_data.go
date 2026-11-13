package web

import (
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/target"
)

type pageData struct {
	Page                string
	RuntimeEngine       string
	Target              target.Identity
	CSRF                string
	User                string
	AuthEnabled         bool
	QualifiedOperations bool // configuration only, never readiness/attestation
	OperationHistory    bool // explicit S1 configuration only, never source approval
}

type runtimePageData interface {
	setRuntimeEngine(string)
	setTarget(target.Identity)
	setQualifiedOperations(bool)
	setOperationHistory(bool)
}

func (p *pageData) setTarget(identity target.Identity)     { p.Target = identity }
func (p *pageData) setQualifiedOperations(configured bool) { p.QualifiedOperations = configured }
func (p *pageData) setOperationHistory(configured bool)    { p.OperationHistory = configured }

func (p *pageData) setRuntimeEngine(engine string) {
	p.RuntimeEngine = engine
}

type authPageData interface {
	setAuth(csrf, user string, enabled bool)
}

func (p *pageData) setAuth(csrf, user string, enabled bool) {
	p.CSRF = csrf
	p.User = user
	p.AuthEnabled = enabled
}

type dashboardData struct {
	pageData
	Stats                 dashboardStats
	Stacks                []*store.Stack
	Containers            []engine.ContainerInfo
	StacksUnavailable     bool
	ContainersUnavailable bool
	ImagesUnavailable     bool
}

type dashboardStats struct {
	Stacks     int
	Containers int
	Running    int
	Images     int
}

type stackListData struct {
	pageData
	Stacks []*store.Stack
}

type stackNewData struct {
	pageData
	DefaultPkl         string
	Source             string
	Snippets           map[string][]*store.Recipe
	SnippetCompletions []map[string]string
}

type stackDetailData struct {
	pageData
	GitRevision     string
	GitState        string
	Stack           *store.Stack
	Files           []string
	ActiveFile      string
	FileContents    map[string]string
	EngineAlignment engineAlignment
}

type stackEditorData struct {
	pageData
	Stack              *store.Stack
	ActiveFile         string
	PklContent         string
	Files              []string
	Snippets           map[string][]*store.Recipe
	SnippetCompletions []map[string]string
	EngineAlignment    engineAlignment
}

type recipeListData struct {
	pageData
	ByCategory map[string][]*store.Recipe
}

type recipeEditorData struct {
	pageData
	Recipe *store.Recipe
}

type containerListData struct {
	pageData
}

type containerDetailData struct {
	pageData
	Container *engine.ContainerInfo
}

type imageListData struct {
	pageData
}
