// Copyright 2025, Offchain Labs, Inc.
// For license information, see:
// https://github.com/OffchainLabs/nitro/blob/master/LICENSE.md
// SPDX-License-Identifier: BUSL-1.1

pragma solidity ^0.8.13;

import {Script, VmSafe, console} from "forge-std/Script.sol";
import {NestedCall, NestedTarget} from "../src/NestedCall.sol";

contract NestedCallTestScript is Script {
    function setUp() public {}

    function run() public {
        vm.startBroadcast(address(0x3f1Eae7D46d88F08fc2F8ed27FCb2AB183EB2d0E));

        NestedCall nestedCall = new NestedCall();
        NestedTarget nestedTarget = new NestedTarget();

        nestedCall.executeNestedCall(address(nestedTarget));

        vm.stopBroadcast();
    }
}
