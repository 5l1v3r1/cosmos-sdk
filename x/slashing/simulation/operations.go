package simulation

import (
	"errors"
	"math/rand"

	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/simapp/helpers"
	simappparams "github.com/cosmos/cosmos-sdk/simapp/params"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/simulation"
	"github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
)

// Simulation operation weights constants
const (
	OpWeightMsgUnjail = "op_weight_msg_unjail"
)

// WeightedOperations returns all the operations from the module with their respective weights
func WeightedOperations(
	appParams module.AppParams, cdc *codec.Codec, ak types.AccountKeeper,
	bk types.BankKeeper, k keeper.Keeper, sk stakingkeeper.Keeper,
) simulation.WeightedOperations {

	var weightMsgUnjail int
	appParams.GetOrGenerate(cdc, OpWeightMsgUnjail, &weightMsgUnjail, nil,
		func(_ *rand.Rand) {
			weightMsgUnjail = simappparams.DefaultWeightMsgUnjail
		},
	)

	return simulation.WeightedOperations{
		simulation.NewWeightedOperation(
			weightMsgUnjail,
			SimulateMsgUnjail(ak, bk, k, sk),
		),
	}
}

// SimulateMsgUnjail generates a MsgUnjail with random values
// nolint: interfacer
func SimulateMsgUnjail(ak types.AccountKeeper, bk types.BankKeeper, k keeper.Keeper, sk stakingkeeper.Keeper) module.Operation {
	return func(
		r *rand.Rand, app *baseapp.BaseApp, ctx sdk.Context,
		accs []module.Account, chainID string,
	) (module.OperationMsg, []module.FutureOperation, error) {

		validator, ok := stakingkeeper.RandomValidator(r, sk, ctx)
		if !ok {
			return module.NoOpMsg(types.ModuleName), nil, nil // skip
		}

		simAccount, found := module.FindAccount(accs, sdk.AccAddress(validator.GetOperator()))
		if !found {
			return module.NoOpMsg(types.ModuleName), nil, nil // skip
		}

		if !validator.IsJailed() {
			// TODO: due to this condition this message is almost, if not always, skipped !
			return module.NoOpMsg(types.ModuleName), nil, nil
		}

		consAddr := sdk.ConsAddress(validator.GetConsPubKey().Address())
		info, found := k.GetValidatorSigningInfo(ctx, consAddr)
		if !found {
			return module.NoOpMsg(types.ModuleName), nil, nil // skip
		}

		selfDel := sk.Delegation(ctx, simAccount.Address, validator.GetOperator())
		if selfDel == nil {
			return module.NoOpMsg(types.ModuleName), nil, nil // skip
		}

		account := ak.GetAccount(ctx, sdk.AccAddress(validator.GetOperator()))
		spendable := bk.SpendableCoins(ctx, account.GetAddress())

		fees, err := module.RandomFees(r, ctx, spendable)
		if err != nil {
			return module.NoOpMsg(types.ModuleName), nil, err
		}

		msg := types.NewMsgUnjail(validator.GetOperator())

		tx := helpers.GenTx(
			[]sdk.Msg{msg},
			fees,
			helpers.DefaultGenTxGas,
			chainID,
			[]uint64{account.GetAccountNumber()},
			[]uint64{account.GetSequence()},
			simAccount.PrivKey,
		)

		_, res, err := app.Deliver(tx)

		// result should fail if:
		// - validator cannot be unjailed due to tombstone
		// - validator is still in jailed period
		// - self delegation too low
		if info.Tombstoned ||
			ctx.BlockHeader().Time.Before(info.JailedUntil) ||
			validator.TokensFromShares(selfDel.GetShares()).TruncateInt().LT(validator.GetMinSelfDelegation()) {
			if res != nil && err == nil {
				if info.Tombstoned {
					return module.NewOperationMsg(msg, true, ""), nil, errors.New("validator should not have been unjailed if validator tombstoned")
				}
				if ctx.BlockHeader().Time.Before(info.JailedUntil) {
					return module.NewOperationMsg(msg, true, ""), nil, errors.New("validator unjailed while validator still in jail period")
				}
				if validator.TokensFromShares(selfDel.GetShares()).TruncateInt().LT(validator.GetMinSelfDelegation()) {
					return module.NewOperationMsg(msg, true, ""), nil, errors.New("validator unjailed even though self-delegation too low")
				}
			}
			// msg failed as expected
			return module.NewOperationMsg(msg, false, ""), nil, nil
		}

		if err != nil {
			return module.NoOpMsg(types.ModuleName), nil, errors.New(res.Log)
		}

		return module.NewOperationMsg(msg, true, ""), nil, nil
	}
}
