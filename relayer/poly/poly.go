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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/beego/beego/v2/core/logs"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ontio/ontology-crypto/keypair"
	"github.com/ontio/ontology-crypto/signature"
	vconf "github.com/ontio/ontology/consensus/vbft/config"
	"github.com/polynetwork/bridge-common/chains/poly"
	scom "github.com/polynetwork/poly-go-sdk/common"
	"github.com/polynetwork/poly/common"
	"github.com/polynetwork/poly/core/types"
	ccom "github.com/polynetwork/poly/native/service/cross_chain_manager/common"

	"github.com/polynetwork/poly-relayer/config"
	"github.com/polynetwork/poly-relayer/msg"
)

type Submitter struct {
	context.Context
	wg     *sync.WaitGroup
	config *config.SubmitterConfig
	sdk    *poly.SDK
}

func (s *Submitter) Init(config *config.SubmitterConfig) (err error) {
	s.config = config
	s.sdk, err = poly.NewSDK(config.ChainId, config.Nodes, time.Minute, 1)
	if err != nil {
		return
	}
	return nil
}

func (s *Submitter) Submit(msg msg.Message) error {
	return nil
}

func (s *Submitter) Hook(ctx context.Context, wg *sync.WaitGroup, ch <-chan msg.Message) error {
	s.Context = ctx
	s.wg = wg
	return nil
}

func (s *Submitter) Process(msg msg.Message) error {
	return nil
}

func (s *Submitter) Stop() error {
	s.wg.Wait()
	return nil
}

func (s *Submitter) MakeTx(tx *msg.Tx, header, anchor *types.Header, proof string, rawAuditPath []byte) (err error) {
	var (
		sigs []byte
		data []byte
	)
	sigHeader := header

	if anchor != nil && proof != "" {
		sigHeader = anchor
	}
	for _, sig := range sigHeader.SigData {
		temp := make([]byte, len(sig))
		copy(temp, sig)
		s, err := signature.ConvertToEthCompatible(temp)
		if err != nil {
			return fmt.Errorf("MakeTx signature.ConvertToEthCompatible %v", err)
		}
		sigs = append(sigs, s)
	}
	return
}

func (s *Submitter) ComposeTx(tx *msg.Tx) (err error) {
	if tx.PolyHash == "" {
		return fmt.Errorf("ComposeTx: Invalid poly hash")
	}
	if tx.DstPolyEpochStartHeight == 0 {
		return fmt.Errorf("ComposeTx: Dst chain poly height not specified")
	}

	if tx.PolyHeight == 0 {
		tx.PolyHeight, err = s.sdk.Node().GetBlockHeightByTxHash(tx.PolyHash)
		if err != nil {
			return
		}
	}

	hdr, err := s.sdk.Node().GetHeaderByHeight(tx.PolyHeight + 1)
	if err != nil {
		return err
	}

	var (
		anchorHeight uint32
		anchor       *types.Header
		hp           string
	)

	if tx.PolyHeight < tx.DstPolyEpochStartHeight {
		anchorHeight = tx.DstPolyEpochStartHeight + 1
	} else {
		isEpoch, _, err := s.CheckEpoch(tx, hdr)
		if err != nil {
			return err
		}
		if isEpoch {
			anchorHeight = tx.PolyHeight + 2
		}
	}

	if anchorHeight > 0 {
		anchor, err = s.sdk.Node().GetHeaderByHeight(anchorHeight)
		if err != nil {
			return err
		}
		proof, err := s.sdk.Node().GetMerkleProof(tx.PolyHeight+1, anchorHeight)
		if err != nil {
			return err
		}
		hp = proof.AuditPath
	}

	merkleValue, auditPath, evt, err := s.GetPolyParams(tx)
	if err != nil {
		return err
	}

	tx.MerkleValue = merkleValue
	return
}

func (s *Submitter) GetPolyParams(tx *msg.Tx) (param *ccom.ToMerkleValue, path []byte, evt *scom.SmartContactEvent, err error) {
	if tx.PolyHash == "" {
		err = fmt.Errorf("ComposeTx: Invalid poly hash")
		return
	}

	if tx.PolyHeight == 0 {
		tx.PolyHeight, err = s.sdk.Node().GetBlockHeightByTxHash(tx.PolyHash)
		if err != nil {
			return
		}
	}
	evt, err = s.sdk.Node().GetSmartContractEvent(hash)
	if err != nil {
		return
	}

	for _, notify := range evt.Notify {
		if notify.ContractAddress == config.POLY_ENTRANCE_ADDRESS {
			states := notify.States.([]interface{})
			if len(states) > 5 {
				method, _ := states[0].(string)
				if method == "makeProof" {
					proof, e := s.sdk.Node().GetCrossStatesProof(tx.PolyHeight, states[5].(string))
					if e != nil {
						err = fmt.Errorf("GetPolyParams: GetCrossStatesProof error %v", e)
						return
					}
					path, err = hex.DecodeString(proof.AuditPath)
					if err != nil {
						return
					}
					value, _, _, _ := msg.ParseAuditPath(path)
					param = new(ccom.ToMerkleValue)
					err = param.Deserialization(pcom.NewZeroCopySource(value))
					if err != nil {
						logs.Error("GetPolyParams: param.Deserialization error %v", err)
					} else {
						return
					}
				}
			}
		}
	}
	err = fmt.Errorf("Valid ToMerkleValue not found")
	return
}

func (s *Submitter) CheckEpoch(tx *msg.Tx, hdr *types.Header) (epoch bool, pubKeys []byte, err error) {
	if len(tx.DstPolyKeepers) == 0 {
		err = fmt.Errorf("Dst chain poly keeper not provided")
		return
	}
	if hdr.NextBookkeeper == common.ADDRESS_EMPTY {
		return
	}
	info := &vconf.VbftBlockInfo{}
	err = json.Unmarshal(hdr.ConsensusPayload, info)
	if err != nil {
		err = fmt.Errorf("CheckEpoch consensus payload unmarshal error %v", err)
		return
	}
	var bks []keypair.PublicKey
	for _, peer := range info.NewChainConfig.Peers {
		keyStr, _ := hex.DecodeString(peer.ID)
		key, _ := keypair.DeserializePublicKey(keyStr)
		bks = append(bks, key)
	}
	bks = keypair.SortPublicKeys(bks)
	pubKeys = []byte{}
	sink := common.NewZeroCopySink(nil)
	sink.WriteUint64(uint64(len(bks)))
	for _, key := range bks {
		var bytes []byte
		bytes, err = msg.EncodePubKey(key)
		if err != nil {
			return
		}
		pubKeys = append(pubKeys, bytes...)
		bytes, err = msg.EncodeEthPubKey(key)
		if err != nil {
			return
		}
		sink.WriteVarBytes(crypto.Keccak256(bytes[1:])[12:])
	}
	epoch = !bytes.Equal(tx.DstPolyKeepers, sink.Bytes())
	return
}
