// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE.md

package arbtest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"

	"github.com/offchainlabs/nitro/arbcompress"
	"github.com/offchainlabs/nitro/arbos"
	"github.com/offchainlabs/nitro/arbos/arbosState"
	"github.com/offchainlabs/nitro/arbos/arbostypes"
	"github.com/offchainlabs/nitro/arbstate"
	"github.com/offchainlabs/nitro/cmd/chaininfo"
	"github.com/offchainlabs/nitro/daprovider"
	"github.com/offchainlabs/nitro/statetransfer"
	"github.com/offchainlabs/nitro/util/testhelpers/env"
)

func BuildBlock(
	statedb *state.StateDB,
	lastBlockHeader *types.Header,
	chainContext core.ChainContext,
	inbox arbstate.InboxBackend,
	seqBatch []byte,
	runCtx *core.MessageRunContext,
) (*types.Block, error) {
	var delayedMessagesRead uint64
	if lastBlockHeader != nil {
		delayedMessagesRead = lastBlockHeader.Nonce.Uint64()
	}
	inboxMultiplexer := arbstate.NewInboxMultiplexer(inbox, delayedMessagesRead, nil, daprovider.KeysetValidate)

	ctx := context.Background()
	message, err := inboxMultiplexer.Pop(ctx)
	if err != nil {
		return nil, err
	}

	delayedMessagesRead = inboxMultiplexer.DelayedMessagesRead()
	l1Message := message.Message

	batchFetcher := func(uint64) ([]byte, error) {
		return seqBatch, nil
	}
	err = l1Message.FillInBatchGasCost(batchFetcher)
	if err != nil {
		// skip malformed batch posting report
		// nolint:nilerr
		return nil, nil
	}

	block, _, err := arbos.ProduceBlock(
		l1Message, delayedMessagesRead, lastBlockHeader, statedb, chainContext, false, runCtx,
	)
	return block, err
}

// A simple mock inbox multiplexer backend
type inboxBackend struct {
	batchSeqNum           uint64
	batches               [][]byte
	positionWithinMessage uint64
	delayedMessages       [][]byte
}

func (b *inboxBackend) PeekSequencerInbox() ([]byte, common.Hash, error) {
	if len(b.batches) == 0 {
		return nil, common.Hash{}, errors.New("read past end of specified sequencer batches")
	}
	return b.batches[0], common.Hash{}, nil
}

func (b *inboxBackend) GetSequencerInboxPosition() uint64 {
	return b.batchSeqNum
}

func (b *inboxBackend) AdvanceSequencerInbox() {
	b.batchSeqNum++
	if len(b.batches) > 0 {
		b.batches = b.batches[1:]
	}
}

func (b *inboxBackend) GetPositionWithinMessage() uint64 {
	return b.positionWithinMessage
}

func (b *inboxBackend) SetPositionWithinMessage(pos uint64) {
	b.positionWithinMessage = pos
}

func (b *inboxBackend) ReadDelayedInbox(seqNum uint64) (*arbostypes.L1IncomingMessage, error) {
	if seqNum >= uint64(len(b.delayedMessages)) {
		return nil, errors.New("delayed inbox message out of bounds")
	}
	msg, err := arbostypes.ParseIncomingL1Message(bytes.NewReader(b.delayedMessages[seqNum]), nil)
	if err != nil {
		// The bridge won't generate an invalid L1 message,
		// so here we substitute it with a less invalid one for fuzzing.
		msg = &arbostypes.TestIncomingMessageWithRequestId
	}
	return msg, nil
}

// A chain context with no information
type noopChainContext struct {
	chainConfig *params.ChainConfig
}

func (c noopChainContext) Config() *params.ChainConfig {
	return c.chainConfig
}

func (c noopChainContext) Engine() consensus.Engine {
	return nil
}

func (c noopChainContext) GetHeader(common.Hash, uint64) *types.Header {
	return nil
}

func FuzzStateTransition(f *testing.F) {
	f.Fuzz(func(t *testing.T, compressSeqMsg bool, seqMsg []byte, delayedMsg []byte, targetsSeed uint8, runCtxSeed uint8) {
		if len(seqMsg) > 0 && daprovider.IsL1AuthenticatedMessageHeaderByte(seqMsg[0]) {
			return
		}
		chainDb := rawdb.NewMemoryDatabase()
		chainConfig := chaininfo.ArbitrumDevTestChainConfig()
		serializedChainConfig, err := json.Marshal(chainConfig)
		if err != nil {
			panic(err)
		}
		initMessage := &arbostypes.ParsedInitMessage{
			ChainId:               chainConfig.ChainID,
			InitialL1BaseFee:      arbostypes.DefaultInitialL1BaseFee,
			ChainConfig:           chainConfig,
			SerializedChainConfig: serializedChainConfig,
		}
		cacheConfig := core.DefaultCacheConfigWithScheme(env.GetTestStateScheme())
		stateRoot, err := arbosState.InitializeArbosInDatabase(
			chainDb,
			cacheConfig,
			statetransfer.NewMemoryInitDataReader(&statetransfer.ArbosInitializationInfo{}),
			chainConfig,
			nil,
			initMessage,
			0,
			0,
		)
		if err != nil {
			panic(err)
		}
		trieDBConfig := cacheConfig.TriedbConfig()
		statedb, err := state.New(stateRoot, state.NewDatabase(triedb.NewDatabase(chainDb, trieDBConfig), nil))
		if err != nil {
			panic(err)
		}
		genesis := arbosState.MakeGenesisBlock(common.Hash{}, 0, 0, stateRoot, chainConfig)

		// Append a header to the input (this part is authenticated by L1).
		// The first 32 bytes encode timestamp and L1 block number bounds.
		// For simplicity, those are all set to 0.
		// The next 8 bytes encode the after delayed message count.
		delayedMessages := [][]byte{delayedMsg}
		seqBatch := make([]byte, 40)
		binary.BigEndian.PutUint64(seqBatch[8:16], ^uint64(0))
		binary.BigEndian.PutUint64(seqBatch[24:32], ^uint64(0))
		binary.BigEndian.PutUint64(seqBatch[32:40], uint64(len(delayedMessages)))
		if compressSeqMsg {
			seqBatch = append(seqBatch, daprovider.BrotliMessageHeaderByte)
			seqMsgCompressed, err := arbcompress.CompressLevel(seqMsg, 0)
			if err != nil {
				panic(fmt.Sprintf("failed to compress sequencer message: %v", err))
			}
			seqBatch = append(seqBatch, seqMsgCompressed...)
		} else {
			seqBatch = append(seqBatch, seqMsg...)
		}
		inbox := &inboxBackend{
			batchSeqNum:           0,
			batches:               [][]byte{seqBatch},
			positionWithinMessage: 0,
			delayedMessages:       delayedMessages,
		}

		localTarget := rawdb.LocalTarget()
		targets := []rawdb.WasmTarget{localTarget}
		if targetsSeed&1 != 0 {
			targets = append(targets, rawdb.TargetWavm)
		}
		if targetsSeed&2 != 0 && localTarget != rawdb.TargetArm64 {
			targets = append(targets, rawdb.TargetArm64)
		}
		if targetsSeed&4 != 0 && localTarget != rawdb.TargetAmd64 {
			targets = append(targets, rawdb.TargetAmd64)
		}
		if targetsSeed&8 != 0 && localTarget != rawdb.TargetHost {
			targets = append(targets, rawdb.TargetHost)
		}

		runCtxNumber := runCtxSeed % 6
		var runCtx *core.MessageRunContext
		switch runCtxNumber {
		case 0:
			runCtx = core.NewMessageCommitContext(targets)
		case 1:
			runCtx = core.NewMessageReplayContext()
		case 2:
			runCtx = core.NewMessageRecordingContext(targets)
		case 3:
			runCtx = core.NewMessagePrefetchContext()
		case 4:
			runCtx = core.NewMessageEthcallContext()
		case 5:
			runCtx = core.NewMessageGasEstimationContext()
		}

		_, err = BuildBlock(statedb, genesis.Header(), noopChainContext{chainConfig: chaininfo.ArbitrumDevTestChainConfig()}, inbox, seqBatch, runCtx)
		if err != nil {
			// With the fixed header it shouldn't be possible to read a delayed message,
			// and no other type of error should be possible.
			panic(err)
		}
	})
}
