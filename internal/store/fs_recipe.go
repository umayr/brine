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

// NewFSRecipeStore creates an FSRecipeStore rooted at dataDir and seeds
// built-in starter recipes if the recipes directory is new.
func NewFSRecipeStore(dataDir string) (*FSRecipeStore, error) {
	recipesDir := filepath.Join(dataDir, "recipes")
	isNew := false
	if _, err := os.Stat(recipesDir); os.IsNotExist(err) {
		isNew = true
	}
	if err := os.MkdirAll(recipesDir, 0755); err != nil {
		return nil, fmt.Errorf("create recipes dir: %w", err)
	}
	rs := &FSRecipeStore{dataDir: dataDir}
	if isNew {
		_ = rs.seed()
	}
	return rs, nil
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
	return os.WriteFile(s.metaPath(m.Slug), data, 0644)
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
	dir := s.recipeDir(slug)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("recipe %q already exists", slug)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create recipe dir: %w", err)
	}
	if err := os.WriteFile(s.bodyPath(slug), []byte(body), 0644); err != nil {
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
	if err := os.WriteFile(s.bodyPath(slug), []byte(body), 0644); err != nil {
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
	m, err := s.readMeta(slug)
	if err != nil {
		return nil, fmt.Errorf("recipe %q not found", slug)
	}
	m.Name = name
	m.Category = category
	m.Description = description
	m.UpdatedAt = time.Now()
	if err := s.writeMeta(m); err != nil {
		return nil, fmt.Errorf("write recipe meta: %w", err)
	}
	body, _ := os.ReadFile(s.bodyPath(slug))
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
		_, _ = s.Create(r.name, r.category, r.description, r.body)
	}
	return nil
}
