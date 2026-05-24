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
	ctrlbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeHandle implements plugin.Handle for unit tests.
type fakeHandle struct{ ds datalayer.Datastore }

func (f *fakeHandle) Context() context.Context                { return context.Background() }
func (f *fakeHandle) Client() client.Client                   { return nil }
func (f *fakeHandle) ReconcilerBuilder() *ctrlbuilder.Builder { return nil }
func (f *fakeHandle) Datastore() datalayer.Datastore          { return f.ds }

func writeTempConfig(t *testing.T, cfg ModelsConfig) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	f, err := os.CreateTemp(t.TempDir(), "models-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestPollLoadsModels(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempConfig(t, ModelsConfig{
		Models: []ModelConfiguration{{Name: "m1"}, {Name: "m2"}},
	})

	c := NewModelConfigCollector("test", path, ds)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	models := ds.Models()
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d: %v", len(models), models)
	}
}

func TestPollMissingFile(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	c := NewModelConfigCollector("test", "/no/such/file.json", ds)
	if _, err := c.Poll(context.Background()); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestCollectorFactoryWiresDatastore(t *testing.T) {
	ds := datastore.NewFakeDataStore()
	path := writeTempConfig(t, ModelsConfig{Models: []ModelConfiguration{{Name: "m1"}}})

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
