/*
 * Copyright (C) 2021 The poly network Authors
 * This file is part of The poly network library.
 *
 * The  poly network  is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * The  poly network  is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Lesser General Public License for more details.
 * You should have received a copy of the GNU Lesser General Public License
 * along with The poly network .  If not, see <http://www.gnu.org/licenses/>.
 */

package poly

import (
	"time"

	"github.com/polynetwork/bridge-common/base"
	"github.com/polynetwork/bridge-common/chains/poly"
	"github.com/polynetwork/poly-relayer/config"
	"github.com/polynetwork/poly-relayer/msg"
)

type Listener struct {
	sdk    *poly.SDK
	config *config.ListenerConfig
}

func (l *Listener) Init(config *config.ListenerConfig) (err error) {
	l.config = config
	sdk, err := poly.NewSDK(base.POLY, config.Nodes, time.Minute, 1)
	if err != nil {
		return err
	}
	l.sdk = sdk
	return
}

func (l *Listener) Scan(height uint64) (txs []*msg.Tx, err error) {
	events, err := l.sdk.Node().GetSmartContractEventByBlock(uint32(height))
	if err != nil {
		return nil, err
	}

	for _, event := range events {
		for _, notify := range event.Notify {
			if notify.ContractAddress == poly.CCM_ADDRESS {
				states := notify.States.([]interface{})
				method, _ := states[0].(string)
				if method != "makeProof" {
					continue
				}
				tx := new(msg.Tx)
				tx.PolyKey = states[5].(string)
				tx.PolyHeight = uint32(height)
				tx.PolyHash = event.TxHash
				txs = append(txs, tx)
			}
		}
	}

	return
}

func (l *Listener) ScanTx(hash string) (tx *msg.Tx, err error) {
	return
}