/*
Copyright IBM Corp. 2016-2017 All Rights Reserved.

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

package configtx

import (
	"fmt"
	"strings"

	"github.com/hyperledger/fabric/common/policies"
	cb "github.com/hyperledger/fabric/protos/common"
	"github.com/hyperledger/fabric/protos/utils"
)

// authorizeUpdate validates that all modified config has the corresponding modification policies satisfied by the signature set
// it returns a map of the modified config
func (cm *configManager) authorizeUpdate(configUpdateEnv *cb.ConfigUpdateEnvelope) (map[string]comparable, error) {
	if configUpdateEnv == nil {
		return nil, fmt.Errorf("Cannot process nil ConfigUpdateEnvelope")
	}

	config, err := UnmarshalConfigUpdate(configUpdateEnv.ConfigUpdate)
	if err != nil {
		return nil, err
	}

	if config.Header == nil {
		return nil, fmt.Errorf("Must have header set")
	}

	seq := computeSequence(config.WriteSet)
	if err != nil {
		return nil, err
	}

	signedData, err := configUpdateEnv.AsSignedData()
	if err != nil {
		return nil, err
	}

	// Verify config is a sequential update to prevent exhausting sequence numbers
	if seq != cm.sequence+1 {
		return nil, fmt.Errorf("Config sequence number jumped from %d to %d", cm.sequence, seq)
	}

	// Verify config is intended for this globally unique chain ID
	if config.Header.ChannelId != cm.chainID {
		return nil, fmt.Errorf("Config is for the wrong chain, expected %s, got %s", cm.chainID, config.Header.ChannelId)
	}

	configMap, err := mapConfig(config.WriteSet)
	if err != nil {
		return nil, err
	}
	for key, value := range configMap {
		logger.Debugf("Processing key %s with value %v", key, value)
		if key == "[Groups] /Channel" {
			// XXX temporary hack to prevent group evaluation for modification
			continue
		}

		// Ensure the config sequence numbers are correct to prevent replay attacks
		var isModified bool

		oldValue, ok := cm.config[key]
		if ok {
			isModified = !value.equals(oldValue)
		} else {
			if value.version() != seq {
				return nil, fmt.Errorf("Key %v was new, but had an older Sequence %d set", key, value.version())
			}
			isModified = true
		}

		// If a config item was modified, its Version must be set correctly, and it must satisfy the modification policy
		if isModified {
			logger.Debugf("Proposed config item %s on channel %s has been modified", key, cm.chainID)

			if value.version() != seq {
				return nil, fmt.Errorf("Key %s was modified, but its Version %d does not equal current configtx Sequence %d", key, value.version(), seq)
			}

			// Get the modification policy for this config item if one was previously specified
			// or accept it if it is new, as the group policy will be evaluated for its inclusion
			if ok {
				policy, ok := cm.policyForItem(oldValue)
				if !ok {
					return nil, fmt.Errorf("Unexpected missing policy %s for item %s", oldValue.modPolicy(), key)
				}

				// Ensure the policy is satisfied
				if err = policy.Evaluate(signedData); err != nil {
					return nil, fmt.Errorf("Policy for %s not satisfied: %s", key, err)
				}
			}

		}
	}

	// Ensure that any config items which used to exist still exist, to prevent implicit deletion
	for key, _ := range cm.config {
		_, ok := configMap[key]
		if !ok {
			return nil, fmt.Errorf("Missing key %v in new config", key)
		}

	}

	return cm.computeUpdateResult(configMap), nil
}

func (cm *configManager) policyForItem(item comparable) (policies.Policy, bool) {
	if strings.HasPrefix(item.modPolicy(), PathSeparator) {
		return cm.PolicyManager().GetPolicy(item.modPolicy()[1:])
	}

	// path is always at least of length 1
	manager, ok := cm.PolicyManager().Manager(item.path[1:])
	if !ok {
		return nil, ok
	}
	return manager.GetPolicy(item.modPolicy())
}

// computeUpdateResult takes a configMap generated by an update and produces a new configMap overlaying it onto the old config
func (cm *configManager) computeUpdateResult(updatedConfig map[string]comparable) map[string]comparable {
	newConfigMap := make(map[string]comparable)
	for key, value := range cm.config {
		newConfigMap[key] = value
	}

	for key, value := range updatedConfig {
		newConfigMap[key] = value
	}
	return newConfigMap
}

func envelopeToConfigUpdate(configtx *cb.Envelope) (*cb.ConfigUpdateEnvelope, error) {
	payload, err := utils.UnmarshalPayload(configtx.Payload)
	if err != nil {
		return nil, err
	}

	if payload.Header == nil /* || payload.Header.ChannelHeader == nil */ {
		return nil, fmt.Errorf("Envelope must have a Header")
	}

	chdr, err := utils.UnmarshalChannelHeader(payload.Header.ChannelHeader)
	if err != nil {
		return nil, fmt.Errorf("Invalid ChannelHeader")
	}

	if chdr.Type != int32(cb.HeaderType_CONFIG_UPDATE) {
		return nil, fmt.Errorf("Not a tx of type CONFIG_UPDATE")
	}

	configUpdateEnv, err := UnmarshalConfigUpdateEnvelope(payload.Data)
	if err != nil {
		return nil, fmt.Errorf("Error unmarshaling ConfigUpdateEnvelope: %s", err)
	}

	return configUpdateEnv, nil
}
