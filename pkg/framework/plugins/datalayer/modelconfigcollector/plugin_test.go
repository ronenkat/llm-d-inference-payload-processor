/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package modelconfigcollector

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/llm-d/llm-d-inference-payload-processor/pkg/datastore"
	"github.com/llm-d/llm-d-inference-payload-processor/pkg/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-payload-processor/pkg/framework/interface/plugin"
	ctrlbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeHandle implements plugin.Handle for unit tests.
type fakeHandle struct{ ds datalayer.Datastore }

func (f *fakeHandle) Context() context.Context                         { return context.Background() }
func (f *fakeHandle) Client() client.Client                            { return nil }
func (f *fakeHandle) ReconcilerBuilder() *ctrlbuilder.Builder          { return nil }
func (f *fakeHandle) Datastore() datalayer.Datastore                   { return f.ds }
func (f *fakeHandle) Plugin(string) plugin.Plugin                      { return nil }
func (f *fakeHandle) AddPlugin(string, plugin.Plugin)                  {}
func (f *fakeHandle) GetAllPlugins() []plugin.Plugin                   { return nil }
func (f *fakeHandle) GetAllPluginsWithNames() map[string]plugin.Plugin { return nil }

// useFactory creates a collector via CollectorFactory or fails the test.
func useFactory(t *testing.T, path string, ds datalayer.Datastore) *ModelConfigCollector {
	t.Helper()
	rawCfg, _ := json.Marshal(PluginConfig{ModelsPath: path})
	p, err := CollectorFactory("test", rawCfg, &fakeHandle{ds: ds})
	if err != nil {
		t.Fatalf("CollectorFactory: %v", err)
	}
	return p.(*ModelConfigCollector)
}

func writeTempModelsConfig(t *testing.T, cfg ModelsConfig) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return writeTempRaw(t, string(data))
}

func writeTempRaw(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "models-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

// --- Factory-level tests ---

func TestCollectorFactory_InvalidJSON(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	_, err := CollectorFactory("x", json.RawMessage(`not-json`), &fakeHandle{ds: ds})
	if err == nil {
		t.Error("expected error for invalid JSON plugin config, got nil")
	}
}

func TestCollectorFactory_EmptyInput(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	_, err := CollectorFactory("x", json.RawMessage(``), &fakeHandle{ds: ds})
	if err == nil {
		t.Error("expected error for empty plugin config input, got nil")
	}
}

func TestCollectorFactory_MissingModelsPath(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	rawCfg, _ := json.Marshal(PluginConfig{}) // modelsPath omitted → empty string
	_, err := CollectorFactory("x", rawCfg, &fakeHandle{ds: ds})
	if err == nil {
		t.Error("expected error for missing modelsPath, got nil")
	}
}

func TestCollectorFactory_FileNotExist(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	rawCfg, _ := json.Marshal(PluginConfig{ModelsPath: "/no/such/file.json"})
	_, err := CollectorFactory("x", rawCfg, &fakeHandle{ds: ds})
	if err == nil {
		t.Error("expected error for non-existent file, got nil")
	}
}

func TestCollectorFactory_WiresDatastoreAndName(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempModelsConfig(t, ModelsConfig{Models: []ModelConfiguration{{Name: "m1"}}})

	rawCfg, _ := json.Marshal(PluginConfig{ModelsPath: path})
	p, err := CollectorFactory("my-collector", rawCfg, &fakeHandle{ds: ds})
	if err != nil {
		t.Fatalf("CollectorFactory returned error: %v", err)
	}

	c, ok := p.(*ModelConfigCollector)
	if !ok {
		t.Fatalf("expected *ModelConfigCollector, got %T", p)
	}
	if c.ds != ds {
		t.Error("factory did not wire the datastore from the handle")
	}
	if c.TypedName().Name != "my-collector" {
		t.Errorf("expected name %q, got %q", "my-collector", c.TypedName().Name)
	}
}

// --- Poll-level tests ---

func TestPoll_LoadsModels(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempModelsConfig(t, ModelsConfig{
		Models: []ModelConfiguration{{Name: "m1"}, {Name: "m2"}},
	})
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	models := ds.Models()
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d: %v", len(models), models)
	}
}

func TestPoll_InvalidFileContent(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempRaw(t, `this is not valid json {{{`)
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err == nil {
		t.Error("expected error for invalid JSON file content, got nil")
	}
}

// TestPoll_WrongSchema verifies that a file with a type mismatch (e.g. "models" is a string
// instead of an array) causes Poll to return an error.
func TestPoll_WrongSchema(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempRaw(t, `{"models": "not-an-array"}`)
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err == nil {
		t.Error("expected error for wrong-schema file content, got nil")
	}
}

// TestPoll_SkipsEmptyName verifies that entries with an empty name are silently skipped.
func TestPoll_SkipsEmptyName(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempModelsConfig(t, ModelsConfig{
		Models: []ModelConfiguration{{Name: ""}, {Name: "valid-model"}},
	})
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	models := ds.Models()
	if len(models) != 1 || models[0] != "valid-model" {
		t.Errorf("expected only [valid-model], got %v", models)
	}
}

// TestPoll_RemovesStaleModels verifies that models absent from the config file are deleted.
func TestPoll_RemovesStaleModels(t *testing.T) {
	ds := datastore.NewFakeDataStore()

	// Seed a model that will not appear in the config.
	ds.GetOrCreateModel("stale-model")

	path := writeTempModelsConfig(t, ModelsConfig{
		Models: []ModelConfiguration{{Name: "current-model"}},
	})
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	models := ds.Models()
	if len(models) != 1 || models[0] != "current-model" {
		t.Errorf("expected only [current-model], got %v", models)
	}
}

// TestPoll_EmptyModelsListClearsStore verifies that an empty models array removes all existing entries.
func TestPoll_EmptyModelsListClearsStore(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	ds.GetOrCreateModel("m1")
	ds.GetOrCreateModel("m2")

	path := writeTempModelsConfig(t, ModelsConfig{Models: []ModelConfiguration{}})
	c := useFactory(t, path, ds)

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	if models := ds.Models(); len(models) != 0 {
		t.Errorf("expected empty store, got %v", models)
	}
}
