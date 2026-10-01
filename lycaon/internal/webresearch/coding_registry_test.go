package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestProviderCratesIO(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != cratesIOUserAgent {
			t.Fatalf("user-agent = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"crates": []map[string]any{{
				"name": "serde", "description": "serialization", "downloads": 1000,
				"documentation": "https://docs.rs/serde/latest/serde/",
			}},
		})
	})
	spec := cratesIoRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "serde", 5)
	if !out.ok || out.hits[0].URL != "https://crates.io/crates/serde" {
		t.Fatalf("out = %+v", out)
	}
	if len(out.hits) != 2 || out.hits[1].URL != "https://docs.rs/serde/latest/serde/" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderPkgGoDev(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "encoding/json" {
			t.Fatalf("q = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"packagePath": "encoding/json", "modulePath": "std", "version": "v1.26.5",
				"synopsis": "JSON encoding and decoding",
			}},
		})
	})
	spec := packageRegistryShapes["pkg_go_dev"]("pkg_go_dev")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"pkg_go_dev": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "encoding/json", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/encoding/json" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderNpm(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("text"); got != "express" {
			t.Fatalf("text = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"objects": []map[string]any{{
				"package": map[string]any{
					"name": "express", "version": "5.0.0", "description": "web framework",
					"links": map[string]any{"npm": "https://www.npmjs.com/package/express"},
				},
			}},
		})
	})
	spec := packageRegistryShapes["npm"]("npm")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?text=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "express", 5)
	if !out.ok || out.hits[0].URL != "https://www.npmjs.com/package/express" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderJsr(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"scope": "deno", "name": "hono", "description": "web framework", "latestVersion": "1.0.0",
			}},
		})
	})
	spec := packageRegistryShapes["jsr"]("jsr")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?query=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"jsr": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "hono", 5)
	if !out.ok || out.hits[0].Title != "@deno/hono" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderNuget(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id": "Newtonsoft.Json", "version": "13.0.4", "description": "JSON framework",
				"projectUrl": "https://www.newtonsoft.com/json",
			}},
		})
	})
	spec := packageRegistryShapes["nuget"]("nuget")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "Newtonsoft.Json", 5)
	if !out.ok || out.hits[0].URL != "https://www.newtonsoft.com/json" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderRubygems(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "rails", "version": "8.1.0", "info": "web framework", "project_uri": "https://rubygems.org/gems/rails",
			"downloads": 100,
		}})
	})
	spec := packageRegistryShapes["rubygems"]("rubygems")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?query=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "rails", 5)
	if !out.ok || out.hits[0].URL != "https://rubygems.org/gems/rails" {
		t.Fatalf("out = %+v", out)
	}
}

func TestDocsRsCrateURLFallback(t *testing.T) {
	if got := docsRsCrateURL("serde", ""); got != "https://docs.rs/serde" {
		t.Fatalf("got %q", got)
	}
	if got := docsRsCrateURL("serde", "https://serde.rs"); got != "https://docs.rs/serde" {
		t.Fatalf("non-docs.rs documentation should fall back, got %q", got)
	}
}

func TestProviderMavenCentral(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("wt"); got != "json" {
			t.Fatalf("wt = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"docs": []map[string]any{{
					"g": "com.fasterxml.jackson.core", "a": "jackson-databind",
					"latestVersion": "2.19.0", "p": "jar",
				}},
			},
		})
	})
	spec := mavenCentralRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query + "&wt=json", nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "jackson", 5)
	if !out.ok || out.hits[0].Title != "com.fasterxml.jackson.core:jackson-databind" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderPackagist(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"name": "symfony/yaml", "description": "YAML component",
				"url": "https://packagist.org/packages/symfony/yaml", "downloads": 10,
			}},
		})
	})
	spec := packageRegistryShapes["packagist"]("packagist")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "symfony", 5)
	if !out.ok || out.hits[0].URL != "https://packagist.org/packages/symfony/yaml" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderPubDev(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "http" {
			t.Fatalf("q = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"packages": []map[string]any{{"package": "http"}, {"package": "dio"}},
		})
	})
	spec := packageRegistryShapes["pub_dev"]("pub_dev")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"pub_dev": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "http", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/packages/http" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderHex(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "phoenix" {
			t.Fatalf("search = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "phoenix",
			"meta": map[string]any{"description": "web framework"},
		}})
	})
	spec := packageRegistryShapes["hex"]("hex")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?search=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"hex": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "phoenix", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/packages/phoenix" || out.hits[0].Snippet != "web framework" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderCran(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "ggplot2" {
			t.Fatalf("q = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{
				"hits": []map[string]any{{
					"_id": "ggplot2",
					"_source": map[string]any{
						"Title": "Create Elegant Data\nVisualisations", "Version": "3.5.1",
					},
				}},
			},
		})
	})
	spec := cranRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"cran": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "ggplot2", 5)
	if !out.ok || out.hits[0].URL != "https://cran.r-project.org/package=ggplot2" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Snippet != "Create Elegant Data Visualisations · 3.5.1" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderHackage(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept = %q", got)
		}
		if got := r.URL.Query().Get("terms"); got != "lens" {
			t.Fatalf("terms = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "lens"}, {"name": "lens-family"}})
	})
	spec := packageRegistryShapes["hackage"]("hackage")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?terms=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"hackage": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "lens", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/package/lens" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderMetacpan(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "Moose" {
			t.Fatalf("q = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{
				"hits": []map[string]any{
					{"_source": map[string]any{
						"distribution": "Moose", "abstract": "postmodern object system",
						"version": "2.2015", "author": "STEVAN",
					}},
					{"_source": map[string]any{
						"distribution": "Moose", "abstract": "older release", "version": "0.45",
					}},
				},
			},
		})
	})
	spec := metacpanRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"metacpan": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "Moose", 5)
	if !out.ok || len(out.hits) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].URL != "https://metacpan.org/dist/Moose" {
		t.Fatalf("url = %q", out.hits[0].URL)
	}
	if out.hits[0].Snippet != "postmodern object system · 2.2015 · STEVAN" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderNVD(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vulnerabilities": []map[string]any{{
				"cve": map[string]any{
					"id": "CVE-2021-44228", "published": "2021-12-10T00:00:00.000",
					"vulnStatus":   "Analyzed",
					"descriptions": []map[string]any{{"lang": "en", "value": "Log4Shell vulnerability."}},
				},
			}},
		})
	})
	spec := nvdRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?keywordSearch=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "log4j", 5)
	if !out.ok || out.hits[0].URL != "https://nvd.nist.gov/vuln/detail/CVE-2021-44228" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderCSSTricks(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"title": "CSS Grid", "url": "https://css-tricks.com/css-grid/", "subtype": "post",
		}})
	})
	spec := cssTricksRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?search=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "grid", 5)
	if !out.ok || out.hits[0].URL != "https://css-tricks.com/css-grid/" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderCanIUse(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"featureIds": []string{"css-grid", "css-subgrid"}})
	})
	spec := caniuseRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?search=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "grid", 5)
	if !out.ok || out.hits[0].URL != "https://caniuse.com/css-grid" {
		t.Fatalf("out = %+v", out)
	}
}
