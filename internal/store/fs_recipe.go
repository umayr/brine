package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FSRecipeStore is a filesystem-backed RecipeStore.
// Layout: {dataDir}/recipes/{slug}/body.pkl
//
//	{dataDir}/recipes/{slug}/meta.json
type FSRecipeStore struct {
	dataDir string
}

// NewFSRecipeStore seeds only an absent library, publishing the complete result
// without replacing a library created by another process.
func NewFSRecipeStore(dataDir string) (*FSRecipeStore, error) {
	return newFSRecipeStore(dataDir, (*FSRecipeStore).seed, publishRecipes)
}

func newFSRecipeStore(dataDir string, seed func(*FSRecipeStore) error, publish func(string, string) error) (*FSRecipeStore, error) {
	if err := mkdirPrivate(dataDir); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dest := filepath.Join(dataDir, "recipes")
	if info, err := os.Lstat(dest); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("recipe library path is not a directory; inspect it before restarting")
		}
		return &FSRecipeStore{dataDir: dataDir}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect recipe library: %w", err)
	}
	stage, err := os.MkdirTemp(dataDir, ".recipes-attempt-")
	if err != nil {
		return nil, fmt.Errorf("create private recipe staging directory: %w", err)
	}
	if err := os.Mkdir(filepath.Join(stage, "recipes"), 0700); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("stage recipe library: %w", err)
	}
	if err := seed(&FSRecipeStore{dataDir: stage}); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("seed recipe library (restart to retry): %w", err)
	}
	if err := publish(filepath.Join(stage, "recipes"), dest); err != nil {
		return nil, fmt.Errorf("publish recipe library without replacement (staging retained; inspect library before retry): %w", err)
	}
	_ = os.Remove(stage) // only the empty attempt-owned parent remains after success
	return &FSRecipeStore{dataDir: dataDir}, nil
}

func (s *FSRecipeStore) recipeDir(slug string) string {
	return filepath.Join(s.dataDir, "recipes", slug)
}

func (s *FSRecipeStore) bodyPath(slug string) string {
	return filepath.Join(s.recipeDir(slug), "body.pkl")
}

func (s *FSRecipeStore) metaPath(slug string) string {
	return filepath.Join(s.recipeDir(slug), "meta.json")
}

type recipeMeta struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *FSRecipeStore) readMeta(slug string) (*recipeMeta, error) {
	if err := validateRecipeSlug(slug); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.metaPath(slug))
	if err != nil {
		return nil, err
	}
	var m recipeMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse recipe meta: %w", err)
	}
	return &m, nil
}

func (s *FSRecipeStore) writeMeta(m *recipeMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFilePrivate(s.metaPath(m.Slug), data)
}

func (s *FSRecipeStore) List() ([]*Recipe, error) {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "recipes"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read recipes dir: %w", err)
	}
	var recipes []*Recipe
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validateRecipeSlug(entry.Name()) != nil {
			continue // ignore directories that are not valid recipe slugs
		}
		m, err := s.readMeta(entry.Name())
		if err != nil {
			continue
		}
		recipes = append(recipes, &Recipe{
			Slug:        m.Slug,
			Name:        m.Name,
			Category:    m.Category,
			Description: m.Description,
			CreatedAt:   m.CreatedAt,
			UpdatedAt:   m.UpdatedAt,
		})
	}
	return recipes, nil
}

func (s *FSRecipeStore) ListByCategory() (map[string][]*Recipe, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]*Recipe)
	for _, r := range all {
		cat := r.Category
		if cat == "" {
			cat = "Uncategorized"
		}
		result[cat] = append(result[cat], r)
	}
	return result, nil
}

func (s *FSRecipeStore) Get(slug string) (*Recipe, error) {
	m, err := s.readMeta(slug)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("recipe %q not found", slug)
		}
		return nil, err
	}
	body, err := os.ReadFile(s.bodyPath(slug))
	if err != nil {
		return nil, fmt.Errorf("read recipe body: %w", err)
	}
	return &Recipe{
		Slug:        m.Slug,
		Name:        m.Name,
		Category:    m.Category,
		Description: m.Description,
		Body:        string(body),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}, nil
}

func (s *FSRecipeStore) Create(name, category, description, body string) (*Recipe, error) {
	slug := slugify(name)
	if slug == "" {
		return nil, fmt.Errorf("invalid recipe name %q", name)
	}
	if err := validateRecipeSlug(slug); err != nil {
		return nil, err
	}
	dir := s.recipeDir(slug)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("recipe %q already exists", slug)
	}
	if err := mkdirPrivate(dir); err != nil {
		return nil, fmt.Errorf("create recipe dir: %w", err)
	}
	if err := writeFilePrivate(s.bodyPath(slug), []byte(body)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write recipe body: %w", err)
	}
	now := time.Now()
	m := &recipeMeta{
		Slug:        slug,
		Name:        name,
		Category:    category,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.writeMeta(m); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write recipe meta: %w", err)
	}
	return &Recipe{
		Slug:        slug,
		Name:        name,
		Category:    category,
		Description: description,
		Body:        body,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (s *FSRecipeStore) Update(slug, body string) (*Recipe, error) {
	m, err := s.readMeta(slug)
	if err != nil {
		return nil, fmt.Errorf("recipe %q not found", slug)
	}
	if err := writeFilePrivate(s.bodyPath(slug), []byte(body)); err != nil {
		return nil, fmt.Errorf("write recipe body: %w", err)
	}
	m.UpdatedAt = time.Now()
	if err := s.writeMeta(m); err != nil {
		return nil, fmt.Errorf("write recipe meta: %w", err)
	}
	return &Recipe{
		Slug:        m.Slug,
		Name:        m.Name,
		Category:    m.Category,
		Description: m.Description,
		Body:        body,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}, nil
}

func (s *FSRecipeStore) UpdateMeta(slug, name, category, description string) (*Recipe, error) {
	if validateRecipeSlug(slug) != nil {
		return nil, ErrRecipeNotFound
	}
	m, err := s.readMeta(slug)
	if os.IsNotExist(err) {
		return nil, ErrRecipeNotFound
	}
	if err != nil {
		return nil, err
	}
	// The response includes the body; do not change metadata if it cannot be read.
	body, err := os.ReadFile(s.bodyPath(slug))
	if err != nil {
		return nil, fmt.Errorf("read recipe body: %w", err)
	}
	m.Name = name
	m.Category = category
	m.Description = description
	m.UpdatedAt = time.Now()
	if err := s.writeMeta(m); err != nil {
		return nil, fmt.Errorf("write recipe meta: %w", err)
	}
	return &Recipe{
		Slug:        m.Slug,
		Name:        m.Name,
		Category:    m.Category,
		Description: m.Description,
		Body:        string(body),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}, nil
}

func (s *FSRecipeStore) Delete(slug string) error {
	if _, err := s.readMeta(slug); err != nil {
		return fmt.Errorf("recipe %q not found", slug)
	}
	return os.RemoveAll(s.recipeDir(slug))
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]`)

// slugify converts a name to a URL-safe slug.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = slugRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	return s
}

// seed writes a set of built-in starter recipes.
func (s *FSRecipeStore) seed() error {
	type seedRecipe struct {
		name        string
		category    string
		description string
		body        string
	}

	starters := []seedRecipe{
		{
			name:        "Simple Web Service",
			category:    "Web",
			description: "Single HTTP service with port mapping",
			body: `["web"] {
  image = "nginx:alpine"
  ports = List("8080:80")
}`,
		},
		{
			name:        "PostgreSQL Database",
			category:    "Database",
			description: "PostgreSQL with a persistent data volume",
			body: `["postgres"] {
  image = "postgres:16-alpine"
  environment {
    ["POSTGRES_DB"]       = "mydb"
    ["POSTGRES_USER"]     = "user"
    ["POSTGRES_PASSWORD"] = "changeme"
  }
  volumes = List("pgdata:/var/lib/postgresql/data")
}`,
		},
		{
			name:        "Redis Cache",
			category:    "Database",
			description: "Redis with AOF persistence enabled",
			body: `["redis"] {
  image = "redis:7-alpine"
  command = List("redis-server", "--appendonly", "yes")
  volumes = List("redisdata:/data")
}`,
		},
		{
			name:        "Health Check",
			category:    "Patterns",
			description: "HTTP health check configuration block",
			body: `healthcheck {
  test    = List("CMD", "wget", "-qO-", "http://localhost/health")
  interval = "30s"
  timeout  = "5s"
  retries  = 3
}`,
		},
		{
			name:        "Resource Limits",
			category:    "Patterns",
			description: "CPU and memory resource constraints",
			body: `resources {
  limits {
    cpus   = "0.5"
    memory = "256m"
  }
  reservations {
    memory = "128m"
  }
}`,
		},
		{
			name:        "Traefik Labels",
			category:    "Networking",
			description: "Traefik reverse-proxy routing labels",
			body: "labels {\n" +
				"  [\"traefik.enable\"]                                         = \"true\"\n" +
				"  [\"traefik.http.routers.myapp.rule\"]                        = \"Host(`myapp.example.com`)\"\n" +
				"  [\"traefik.http.routers.myapp.entrypoints\"]                 = \"websecure\"\n" +
				"  [\"traefik.http.routers.myapp.tls.certresolver\"]            = \"letsencrypt\"\n" +
				"  [\"traefik.http.services.myapp.loadbalancer.server.port\"]   = \"8080\"\n" +
				"}",
		},
	}

	for _, r := range starters {
		if _, err := s.Create(r.name, r.category, r.description, r.body); err != nil {
			return fmt.Errorf("create starter recipe: %w", err)
		}
	}
	return nil
}
