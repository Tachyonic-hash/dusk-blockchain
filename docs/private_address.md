## Private Address Specification
privatekey = {}(32 byte)

doublehash = sha3(sha3(privatekey)) = {}(32 byte)

checksum = doublehash[0:4] = {}(4 byte)

prefix = {}(? byte)

privateaddress = prefix + seed + checksum = {}(? byte)

privateaddressWIF = base58(privatekey) = {}(? byte)

