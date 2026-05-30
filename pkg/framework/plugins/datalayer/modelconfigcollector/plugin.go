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
	"errors"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-payload-processor/pkg/framework/interface/datalayer"
	dlsrc "github.com/llm-d/llm-d-inference-payload-processor/pkg/framework/interface/datalayer/datasource"
	"github.com/llm-d/llm-d-inference-payload-processor/pkg/framework/interface/plugin"
)

const PluginType = "model-config-collector"

// compile-time interface assertion
var _ dlsrc.Collector = &ModelConfigCollector{}

// PluginConfig holds the JSON plugin configuration for this collector.
type PluginConfig struct {
	ModelsPath string `json:"modelsPath"`
}

// ModelConfiguration is a single model entry in the config file.
type ModelConfiguration struct {
	Name string `json:"name"`
}

// ModelsConfig is the schema of the JSON config file.
type ModelsConfig struct {
	Models []ModelConfiguration `json:"models"`
}

// ModelConfigCollector reads a JSON file listing model names and registers
// each model in the datastore on every poll.
type ModelConfigCollector struct {
	typedName  plugin.TypedName
	ds         datalayer.Datastore
	modelsPath string
}

// CollectorFactory creates a ModelConfigCollector from the plugin handle and raw JSON config.
// It validates that modelsPath is set and that the file exists; content parsing happens in Poll.
func CollectorFactory(name string, rawCfg json.RawMessage, h plugin.Handle) (plugin.Plugin, error) {
	var cfg PluginConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	if cfg.ModelsPath == "" {
		return nil, errors.New("modelsPath is required")
	}
	if _, err := os.Stat(cfg.ModelsPath); err != nil {
		return nil, err
	}
	return NewModelConfigCollector(name, cfg.ModelsPath, h.Datastore()), nil
}

// NewModelConfigCollector constructs a ModelConfigCollector wired to ds.
func NewModelConfigCollector(name, modelsPath string, ds datalayer.Datastore) *ModelConfigCollector {
	return &ModelConfigCollector{
		typedName:  plugin.TypedName{Type: PluginType, Name: name},
		ds:         ds,
		modelsPath: modelsPath,
	}
}

func (c *ModelConfigCollector) TypedName() plugin.TypedName { return c.typedName }

// Poll reads the config file, registers every valid listed model in the datastore,
// and removes any datastore model that no longer appears in the file.
func (c *ModelConfigCollector) Poll(ctx context.Context) (any, error) {
	logger := log.FromContext(ctx).WithName("model-config-collector")

	data, err := os.ReadFile(c.modelsPath)
	if err != nil {
		return nil, err
	}

	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	desired := make(map[string]struct{}, len(cfg.Models))
	for _, m := range cfg.Models {
		if m.Name == "" {
			logger.Info("skipping model entry with empty name")
			continue
		}
		desired[m.Name] = struct{}{}
		c.ds.GetOrCreateModel(m.Name)
	}

	for _, existing := range c.ds.Models() {
		if _, ok := desired[existing]; !ok {
			logger.Info("removing model no longer present in config", "model", existing)
			c.ds.DeleteModel(existing)
		}
	}

	return nil, nil
}
