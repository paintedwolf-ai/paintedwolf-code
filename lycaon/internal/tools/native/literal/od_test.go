package literal

import (
	"strings"
	"testing"
)

// odFiles hold every byte class od renders.
var odFiles = map[string]string{
	"o":     "Hello, world!\n\x00\x01\x7f\x80\xff abcdefghijklmnopqrstuvwxyz0123456789",
	"zeros": strings.Repeat("\x00", 64),
	"empty": "",
}

func TestOdFormsProduceExactBytes(t *testing.T) {
	h := newHarness(t, odFiles)
	for _, c := range []formCase{
		{argv: "od -A x -t x1 -N 16 o", stdout: "000000 48 65 6c 6c 6f 2c 20 77 6f 72 6c 64 21 0a 00 01\n000010\n"},
		{argv: "od o", stdout: "0000000 062510 066154 026157 073440 071157 062154 005041 000400\n0000020 100177 020377 061141 062143 063145 064147 065151 066153\n0000040 067155 070157 071161 072163 073165 074167 075171 030460\n0000060 031462 032464 033466 034470\n0000070\n"},
		{argv: "od -c o", stdout: "0000000   H   e   l   l   o   ,       w   o   r   l   d   !  \\n  \\0 001\n0000020 177 200 377       a   b   c   d   e   f   g   h   i   j   k   l\n0000040   m   n   o   p   q   r   s   t   u   v   w   x   y   z   0   1\n0000060   2   3   4   5   6   7   8   9\n0000070\n"},
		{argv: "od -t a o", stdout: "0000000   H   e   l   l   o   ,  sp   w   o   r   l   d   !  nl nul soh\n0000020 del nul del  sp   a   b   c   d   e   f   g   h   i   j   k   l\n0000040   m   n   o   p   q   r   s   t   u   v   w   x   y   z   0   1\n0000060   2   3   4   5   6   7   8   9\n0000070\n"},
		{argv: "od -t x2 o", stdout: "0000000 6548 6c6c 2c6f 7720 726f 646c 0a21 0100\n0000020 807f 20ff 6261 6463 6665 6867 6a69 6c6b\n0000040 6e6d 706f 7271 7473 7675 7877 7a79 3130\n0000060 3332 3534 3736 3938\n0000070\n"},
		{argv: "od -t x4 -A d o", stdout: "0000000 6c6c6548 77202c6f 646c726f 01000a21\n0000016 20ff807f 64636261 68676665 6c6b6a69\n0000032 706f6e6d 74737271 78777675 31307a79\n0000048 35343332 39383736\n0000056\n"},
		{argv: "od -t d1 -N 8 o", stdout: "0000000   72  101  108  108  111   44   32  119\n0000010\n"},
		{argv: "od -t u1 -N 8 o", stdout: "0000000  72 101 108 108 111  44  32 119\n0000010\n"},
		{argv: "od -t o1 -N 8 o", stdout: "0000000 110 145 154 154 157 054 040 167\n0000010\n"},
		{argv: "od -x -N 5 o", stdout: "0000000 6548 6c6c 006f\n0000005\n"},
		{argv: "od -c -t x1 -N 20 o", stdout: "0000000   H   e   l   l   o   ,       w   o   r   l   d   !  \\n  \\0 001\n         48  65  6c  6c  6f  2c  20  77  6f  72  6c  64  21  0a  00  01\n0000020 177 200 377    \n         7f  80  ff  20\n0000024\n"},
		{argv: "od -A n -t x1 -N 5 o", stdout: " 48 65 6c 6c 6f\n"},
		{argv: "od -An -tx1 -N3 o", stdout: " 48 65 6c\n"},
		{argv: "od -j 3 -N 4 -c o", stdout: "0000003   l   o   ,    \n0000007\n"},
		{argv: "od zeros", stdout: "0000000 000000 000000 000000 000000 000000 000000 000000 000000\n*\n0000100\n"},
		{argv: "od -v -A x -t x1 zeros", stdout: "000000 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00\n000010 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00\n000020 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00\n000030 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00\n000040\n"},
		{argv: "od -t x4", stdin: "abc", stdout: "0000000 00636261\n0000003\n"},
		{argv: "od empty", stdout: "0000000\n"},
		{argv: "od -b -N 4 o", stdout: "0000000 110 145 154 154\n0000004\n"},
		{argv: "od -d -N 4 o", stdout: "0000000 25928 27756\n0000004\n"},
		{argv: "od -d", stdin: "\xff\xff\x01\x00", stdout: "0000000 65535     1\n0000004\n"},
		{argv: "od -s", stdin: "\xff\xff\x01\x00", stdout: "0000000     -1      1\n0000004\n"},
		{argv: "od -t u4 -N 8 o", stdout: "0000000 1819043144 1998597231\n0000010\n"},
		{argv: "od -t d8 -N 8 o", stdout: "0000000  8583909746840200520\n0000010\n"},
		{argv: "od -t o8 -N 8 o", stdout: "0000000 0734401306755433062510\n0000010\n"},
		{argv: "od -o -N 4 o", stdout: "0000000 062510 066154\n0000004\n"},
		{argv: "od -s -N 6 o", stdout: "0000000  25928  27756  11375\n0000006\n"},
		{argv: "od -t d2 -N 6 o", stdout: "0000000  25928  27756  11375\n0000006\n"},
		{argv: "od -t dS -N 4 o", stdout: "0000000  25928  27756\n0000004\n"},
		{argv: "od -t xL -N 8 o", stdout: "0000000 77202c6f6c6c6548\n0000010\n"},
		{argv: "od -t x2 -t c", stdin: "ab", stdout: "0000000    6261\n          a   b\n0000002\n"},
		{argv: "od -t x8 -t u1", stdin: "abcdefghijklmnopq", stdout: "0000000                6867666564636261                706f6e6d6c6b6a69\n         97  98  99 100 101 102 103 104 105 106 107 108 109 110 111 112\n0000020                0000000000000071\n        113\n0000021\n"},
		{argv: "od -t d1", stdin: "\x80\xff\x01", stdout: "0000000 -128   -1    1\n0000003\n"},
		{argv: "od -j 100 o", stderr: "od: cannot skip past end of combined input\n", status: 1},
		{argv: "od -t x1 nope empty", stdout: "0000000\n", stderr: "od: nope: No such file or directory\n", status: 1},
		{argv: "od -t x1 -N 0x2 -j 010 o", stdout: "0000010 6f 72\n0000012\n"},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

func TestOdRefusesUnsupportedTypes(t *testing.T) {
	h := newHarness(t, odFiles)
	for _, argv := range []string{"od -t f4 o", "od -t x3 o", "od -t x1z o", "od -A q o", "od -N x o", "od -j -1 o"} {
		h.exec("", strings.Fields(argv)...).refused(t, argv, "COMMAND_OPERAND_INVALID")
	}
}
