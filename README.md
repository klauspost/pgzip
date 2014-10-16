pgzip
=====

Go parallel gzip compression. This is a fully gzip compatible drop in replacement for "compress/gzip".

This will split compression into blocks that are compressed in parallel. This can be useful for compressing big amounts of data.

The gzip decompression has not been modified, but remains in the package, so you can use it as a complete replacement for "compress/gzip".

A variant of this is [bgzf](http://godoc.org/code.google.com/p/biogo.bam/bgzf), which has the same feature, as well as seeking in the resulting file. The only drawback is a slightly bigger overhead compared to gzip.

[![GoDoc][1]][2] [![Build Status][3]][4]

[1]: https://godoc.org/github.com/klauspost/pgzip?status.svg
[2]: https://godoc.org/github.com/klauspost/pgzip
[3]: https://travis-ci.org/klauspost/pgzip.svg
[4]: https://travis-ci.org/klauspost/pgzip

Installation
====
```go get github.com/klauspost/pgzip```

Usage
====

To use as a replacement for gzip, exchange import ```compress/gzip``` with ```import gzip github.com/klauspost/pgzip```. The API should be compatible.

To change the block size, use the added (*pgzip.Writer).SetConcurrency(blockSize, blocks int) function. With this you can control the approximate size of your blocks, as well as how many you want to be processing in parallel. Default values for this is SetConcurrency(250000, 16), meaning blocks are split at 250000 bytes and up to 16 blocks can be processing at once before the writer blocks.


Example:
```
var b bytes.Buffer
w := gzip.NewWriter(&b)
w.SetConcurrency(100000, 10)
w.Write([]byte("hello, world\n"))
w.Close()
```

To get any performance gains, you should at least be compressing more than 1 megabyte of data at the time.

You should at least have a block size of 100k and at least a number of blocks that match the number of cores your would like to utilize, but about twice the number of blocks would be the best.

Another side effect of this is, that it is likely to speed up your other code, since writes to the compressor only blocks if the compressor is already compressing the number of blocks you have specified. This also means you don't have worry about buffering input to the compressor.

Performance
====
Compression cost is usually about 0.2% with default settings with a block size of 250k.

Example with GOMAXPROC set to 4 (dual core with 2 hyperthreads)

Compressor  | MB/sec   | speedup | size | size overhead
------------|----------|---------|------|---------
gzip (golang) | 15.082MB/s | 1.0x | 6.405.193 | 0%
pgzip (golang) | 26.736MB/s|1.8x | 6.421.585 | 0.2%
bgzf (golang) | 29.525MB/s | 1.9x | 6.875.913 | 7.3%


