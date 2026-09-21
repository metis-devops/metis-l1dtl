// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

// Test fixtures, not production contracts. Events match the Metis ingestion ABI.
contract AddressManagerFixture {
    event AddressSet(string indexed name, address newAddress, address oldAddress);
    mapping(bytes32 => address) public addresses;

    function setAddress(string calldata name, address newAddress) external {
        bytes32 key = keccak256(bytes(name));
        address oldAddress = addresses[key];
        addresses[key] = newAddress;
        emit AddressSet(name, newAddress, oldAddress);
    }
}

contract CTCFixture {
    event TransactionEnqueued(
        uint256 chain,
        address indexed origin,
        address indexed target,
        uint256 gasLimit,
        bytes data,
        uint256 indexed index,
        uint256 timestamp
    );
    mapping(uint256 => uint256) public nextIndex;

    function enqueue(uint256 chain, address target, uint256 gasLimit, bytes calldata data) external {
        emit TransactionEnqueued(chain, msg.sender, target, gasLimit, data, nextIndex[chain]++, block.timestamp);
    }

    // Explicit indices exercise upgrades, gaps and conflicts without storage RPC overrides.
    function enqueueAt(uint256 chain, address target, uint256 gasLimit, bytes calldata data, uint256 index) external {
        nextIndex[chain] = index + 1;
        emit TransactionEnqueued(chain, msg.sender, target, gasLimit, data, index, block.timestamp);
    }
}
