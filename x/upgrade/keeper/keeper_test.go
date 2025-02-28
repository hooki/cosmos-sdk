package keeper_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"

	"cosmossdk.io/depinject"
	"github.com/cosmos/cosmos-sdk/baseapp"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/upgrade/keeper"
	"github.com/cosmos/cosmos-sdk/x/upgrade/testutil"
	"github.com/cosmos/cosmos-sdk/x/upgrade/types"
)

type KeeperTestSuite struct {
	suite.Suite

	baseApp       *baseapp.BaseApp
	upgradeKeeper keeper.Keeper
	homeDir       string
	ctx           sdk.Context
	msgSrvr       types.MsgServer
	addrs         []sdk.AccAddress
}

func (s *KeeperTestSuite) SetupTest() {
	var (
		bankKeeper    bankkeeper.Keeper
		stakingKeeper *stakingkeeper.Keeper
	)

	homeDir := filepath.Join(s.T().TempDir(), "x_upgrade_keeper_test")
	appConfig := depinject.Configs(testutil.AppConfig, depinject.Supply(simtestutil.NewAppOptionsWithFlagHome(homeDir)))
	app, err := simtestutil.Setup(appConfig, &s.upgradeKeeper, &bankKeeper, &stakingKeeper)
	s.NoError(err)
	s.upgradeKeeper.SetVersionSetter(app.BaseApp)

	s.T().Log("home dir:", homeDir)
	s.homeDir = homeDir
	s.ctx = app.BaseApp.NewContext(false, tmproto.Header{
		Time:   time.Now(),
		Height: 10,
	})
	s.msgSrvr = keeper.NewMsgServerImpl(s.upgradeKeeper)
	s.addrs = simtestutil.AddTestAddrsIncremental(bankKeeper, stakingKeeper, s.ctx, 1, sdk.NewInt(30000000))
	s.baseApp = app.BaseApp
}

func (s *KeeperTestSuite) TestReadUpgradeInfoFromDisk() {
	// require no error when the upgrade info file does not exist
	_, err := s.upgradeKeeper.ReadUpgradeInfoFromDisk()
	s.Require().NoError(err)

	expected := types.Plan{
		Name:   "test_upgrade",
		Height: 100,
	}

	// create an upgrade info file
	s.Require().NoError(s.upgradeKeeper.DumpUpgradeInfoToDisk(101, expected))

	ui, err := s.upgradeKeeper.ReadUpgradeInfoFromDisk()
	s.Require().NoError(err)
	expected.Height = 101
	s.Require().Equal(expected, ui)
}

func (s *KeeperTestSuite) TestScheduleUpgrade() {
	cases := []struct {
		name    string
		plan    types.Plan
		setup   func()
		expPass bool
	}{
		{
			name: "successful height schedule",
			plan: types.Plan{
				Name:   "all-good",
				Info:   "some text here",
				Height: 123450000,
			},
			setup:   func() {},
			expPass: true,
		},
		{
			name: "successful overwrite",
			plan: types.Plan{
				Name:   "all-good",
				Info:   "some text here",
				Height: 123450000,
			},
			setup: func() {
				s.upgradeKeeper.ScheduleUpgrade(s.ctx, types.Plan{
					Name:   "alt-good",
					Info:   "new text here",
					Height: 543210000,
				})
			},
			expPass: true,
		},
		{
			name: "unsuccessful schedule: invalid plan",
			plan: types.Plan{
				Height: 123450000,
			},
			setup:   func() {},
			expPass: false,
		},
		{
			name: "unsuccessful height schedule: due date in past",
			plan: types.Plan{
				Name:   "all-good",
				Info:   "some text here",
				Height: 1,
			},
			setup:   func() {},
			expPass: false,
		},
		{
			name: "unsuccessful schedule: schedule already executed",
			plan: types.Plan{
				Name:   "all-good",
				Info:   "some text here",
				Height: 123450000,
			},
			setup: func() {
				s.upgradeKeeper.SetUpgradeHandler("all-good", func(ctx sdk.Context, plan types.Plan, vm module.VersionMap) (module.VersionMap, error) {
					return vm, nil
				})
				s.upgradeKeeper.ApplyUpgrade(s.ctx, types.Plan{
					Name:   "all-good",
					Info:   "some text here",
					Height: 123450000,
				})
			},
			expPass: false,
		},
	}

	for _, tc := range cases {
		tc := tc

		s.Run(tc.name, func() {
			// reset suite
			s.SetupTest()

			// setup test case
			tc.setup()

			err := s.upgradeKeeper.ScheduleUpgrade(s.ctx, tc.plan)

			if tc.expPass {
				s.Require().NoError(err, "valid test case failed")
			} else {
				s.Require().Error(err, "invalid test case passed")
			}
		})
	}
}

func (s *KeeperTestSuite) TestSetUpgradedClient() {
	cs := []byte("IBC client state")

	cases := []struct {
		name   string
		height int64
		setup  func()
		exists bool
	}{
		{
			name:   "no upgraded client exists",
			height: 10,
			setup:  func() {},
			exists: false,
		},
		{
			name:   "success",
			height: 10,
			setup: func() {
				s.upgradeKeeper.SetUpgradedClient(s.ctx, 10, cs)
			},
			exists: true,
		},
	}

	for _, tc := range cases {
		// reset suite
		s.SetupTest()

		// setup test case
		tc.setup()

		gotCs, exists := s.upgradeKeeper.GetUpgradedClient(s.ctx, tc.height)
		if tc.exists {
			s.Require().Equal(cs, gotCs, "valid case: %s did not retrieve correct client state", tc.name)
			s.Require().True(exists, "valid case: %s did not retrieve client state", tc.name)
		} else {
			s.Require().Nil(gotCs, "invalid case: %s retrieved valid client state", tc.name)
			s.Require().False(exists, "invalid case: %s retrieved valid client state", tc.name)
		}
	}
}

// Test that the protocol version successfully increments after an
// upgrade and is successfully set on BaseApp's appVersion.
func (s *KeeperTestSuite) TestIncrementProtocolVersion() {
	oldProtocolVersion := s.baseApp.AppVersion()
	s.upgradeKeeper.SetUpgradeHandler("dummy", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) { return vm, nil })
	dummyPlan := types.Plan{
		Name:   "dummy",
		Info:   "some text here",
		Height: 100,
	}
	s.upgradeKeeper.ApplyUpgrade(s.ctx, dummyPlan)
	upgradedProtocolVersion := s.baseApp.AppVersion()

	s.Require().Equal(oldProtocolVersion+1, upgradedProtocolVersion)
}

// Tests that the underlying state of x/upgrade is set correctly after
// an upgrade.
func (s *KeeperTestSuite) TestMigrations() {
	initialVM := module.VersionMap{"bank": uint64(1)}
	s.upgradeKeeper.SetModuleVersionMap(s.ctx, initialVM)
	vmBefore := s.upgradeKeeper.GetModuleVersionMap(s.ctx)
	s.upgradeKeeper.SetUpgradeHandler("dummy", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		// simulate upgrading the bank module
		vm["bank"] = vm["bank"] + 1
		return vm, nil
	})
	dummyPlan := types.Plan{
		Name:   "dummy",
		Info:   "some text here",
		Height: 123450000,
	}

	s.upgradeKeeper.ApplyUpgrade(s.ctx, dummyPlan)
	vm := s.upgradeKeeper.GetModuleVersionMap(s.ctx)
	s.Require().Equal(vmBefore["bank"]+1, vm["bank"])
}

func (s *KeeperTestSuite) TestLastCompletedUpgrade() {
	keeper := s.upgradeKeeper
	require := s.Require()

	s.T().Log("verify empty name if applied upgrades are empty")
	name, height := keeper.GetLastCompletedUpgrade(s.ctx)
	require.Equal("", name)
	require.Equal(int64(0), height)

	keeper.SetUpgradeHandler("test0", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})

	keeper.ApplyUpgrade(s.ctx, types.Plan{
		Name:   "test0",
		Height: 10,
	})

	s.T().Log("verify valid upgrade name and height")
	name, height = keeper.GetLastCompletedUpgrade(s.ctx)
	require.Equal("test0", name)
	require.Equal(int64(10), height)

	keeper.SetUpgradeHandler("test1", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})

	newCtx := s.ctx.WithBlockHeight(15)
	keeper.ApplyUpgrade(newCtx, types.Plan{
		Name:   "test1",
		Height: 15,
	})

	s.T().Log("verify valid upgrade name and height with multiple upgrades")
	name, height = keeper.GetLastCompletedUpgrade(newCtx)
	require.Equal("test1", name)
	require.Equal(int64(15), height)
}

// This test ensures that `GetLastDoneUpgrade` always returns the last upgrade according to the block height
// it was executed at, rather than using an ordering based on upgrade names.
func (s *KeeperTestSuite) TestLastCompletedUpgradeOrdering() {
	keeper := s.upgradeKeeper
	require := s.Require()

	// apply first upgrade
	keeper.SetUpgradeHandler("test-v0.9", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})

	keeper.ApplyUpgrade(s.ctx, types.Plan{
		Name:   "test-v0.9",
		Height: 10,
	})

	name, height := keeper.GetLastCompletedUpgrade(s.ctx)
	require.Equal("test-v0.9", name)
	require.Equal(int64(10), height)

	// apply second upgrade
	keeper.SetUpgradeHandler("test-v0.10", func(_ sdk.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})

	newCtx := s.ctx.WithBlockHeight(15)
	keeper.ApplyUpgrade(newCtx, types.Plan{
		Name:   "test-v0.10",
		Height: 15,
	})

	name, height = keeper.GetLastCompletedUpgrade(newCtx)
	require.Equal("test-v0.10", name)
	require.Equal(int64(15), height)
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}
