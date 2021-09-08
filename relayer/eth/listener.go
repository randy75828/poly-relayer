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

package eth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/beego/beego/v2/core/logs"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/polynetwork/bridge-common/abi/eccm_abi"
	"github.com/polynetwork/bridge-common/base"
	"github.com/polynetwork/bridge-common/chains"
	"github.com/polynetwork/bridge-common/chains/eth"
	"github.com/polynetwork/bridge-common/chains/poly"
	"github.com/polynetwork/poly-relayer/config"
	"github.com/polynetwork/poly-relayer/msg"
	pcom "github.com/polynetwork/poly/common"
	ccom "github.com/polynetwork/poly/native/service/cross_chain_manager/common"
	ceth "github.com/polynetwork/poly/native/service/cross_chain_manager/eth"
)

type Listener struct {
	sdk            *eth.SDK
	poly           *poly.SDK
	ccm            common.Address
	ccd            common.Address
	config         *config.ListenerConfig
	GetProofHeight func() (uint64, error)
	GetProof       func([]byte, uint64) (uint64, []byte, error)
	name           string
}

func (l *Listener) Init(config *config.ListenerConfig, poly *poly.SDK) (err error) {
	l.config = config
	l.name = base.GetChainName(config.ChainId)
	l.ccm = common.HexToAddress(config.CCMContract)
	l.ccd = common.HexToAddress(config.CCDContract)
	l.poly = poly
	if poly == nil {
		return fmt.Errorf("Poly sdk instance should be provided for the listener of %s", l.name)
	}
	// Common
	l.GetProofHeight = l.getProofHeight
	l.GetProof = l.getProof

	l.sdk = eth.WithOptions(config.ChainId, config.Nodes, time.Minute, 1)
	return
}

func (l *Listener) getProofHeight() (height uint64, err error) {
	switch l.config.ChainId {
	case base.ETH, base.BSC, base.HECO, base.O3:
		h, err := l.poly.Node().GetSideChainHeight(l.config.ChainId)
		if err != nil {
			return 0, err
		}
		height = h - base.BlocksToWait(l.config.ChainId)
	case base.OK:
		h, err := l.sdk.Node().GetLatestHeight()
		if err != nil {
			return 0, err
		}
		height = h - 3
	default:
		return 0, fmt.Errorf("getProofHeight unsupported chain %s", l.name)
	}
	return
}

func (l *Listener) getProof(txId []byte, txHeight uint64) (height uint64, proof []byte, err error) {
	id := msg.EncodeTxId(txId)
	bytes, err := ceth.MappingKeyAt(id, "01")
	if err != nil {
		err = fmt.Errorf("%s scan event mapping key error %v", l.name, err)
		return
	}
	proofKey := hexutil.Encode(bytes)
	height, err = l.GetProofHeight()
	if err != nil {
		err = fmt.Errorf("%s can height get proof height error %v", l.name, err)
		return
	}
	if txHeight >= height {
		err = fmt.Errorf("%w Proof not ready", msg.ERR_PROOF_UNAVAILABLE)
		return
	}
	ethProof, err := l.sdk.Node().GetProof(l.ccd.String(), proofKey, height)
	if err != nil {
		return 0, nil, err
	}
	proof, err = json.Marshal(ethProof)
	return
}

func (l *Listener) Compose(tx *msg.Tx) (err error) {
	if tx.SrcHeight == 0 || len(tx.TxId) == 0 {
		return fmt.Errorf("tx missing attributes src height %v, txid %s", tx.SrcHeight, tx.TxId)
	}
	if len(tx.SrcParam) == 0 {
		return fmt.Errorf("src param is missing")
	}
	event, err := hex.DecodeString(tx.SrcParam)
	if err != nil {
		return fmt.Errorf("%s submitter decode src param error %v event %s", l.name, err, tx.SrcParam)
	}
	txId, err := hex.DecodeString(tx.TxId)
	if err != nil {
		return fmt.Errorf("%s failed to decode src txid %s, err %v", l.name, tx.TxId, err)
	}
	param := &ccom.MakeTxParam{}
	err = param.Deserialization(pcom.NewZeroCopySource(event))
	if err != nil {
		return
	}
	tx.Param = param
	tx.SrcEvent = event
	tx.SrcProofHeight, tx.SrcProof, err = l.GetProof(txId, tx.SrcHeight)
	return
}

func (l *Listener) Header(height uint64) (header []byte, hash []byte, err error) {
	logs.Info("Fetching %s block %d header", l.name, height)
	hdr, err := l.sdk.Node().HeaderByNumber(context.Background(), big.NewInt(int64(height)))
	if err != nil {
		err = fmt.Errorf("Fetch block header error %v", err)
		return nil, nil, err
	}
	hash = hdr.Hash().Bytes()
	header, err = hdr.MarshalJSON()
	return
}

func (l *Listener) Scan(height uint64) (txs []*msg.Tx, err error) {
	ccm, err := eccm_abi.NewEthCrossChainManager(l.ccm, l.sdk.Node())
	if err != nil {
		return nil, err
	}
	opt := &bind.FilterOpts{
		Start:   height,
		End:     &height,
		Context: context.Background(),
	}
	events, err := ccm.FilterCrossChainEvent(opt, nil)
	if err != nil {
		return nil, err
	}

	if events == nil {
		return
	}

	txs = []*msg.Tx{}
	for events.Next() {
		ev := events.Event
		param := &ccom.MakeTxParam{}
		err = param.Deserialization(pcom.NewZeroCopySource([]byte(ev.Rawdata)))
		if err != nil {
			return
		}
		tx := &msg.Tx{
			TxId:       msg.EncodeTxId(ev.TxId),
			SrcHash:    ev.Raw.TxHash.String(),
			DstChainId: ev.ToChainId,
			SrcHeight:  height,
			SrcParam:   hex.EncodeToString(ev.Rawdata),
		}
		txs = append(txs, tx)
	}

	return
}

func (l *Listener) ScanTx(hash string) (tx *msg.Tx, err error) {
	return
}

func (l *Listener) ListenCheck() time.Duration {
	duration := time.Second
	if l.config.ListenCheck > 0 {
		duration = time.Duration(l.config.ListenCheck) * time.Second
	}
	return duration
}

func (l *Listener) Nodes() chains.Nodes {
	return l.sdk.ChainSDK
}

func (l *Listener) ChainId() uint64 {
	return l.config.ChainId
}

func (l *Listener) Defer() int {
	return l.config.Defer
}