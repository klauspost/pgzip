pgzip
=====

Go parallel gzip compression. This is a drop in replacement for "compress/gzip".

This will split compression into blocks that are compressed in parallel. This can be useful for compressing big amounts of data.

The gzip decompression has not been modified, but remains in the package, so you can use it as a complete replacement for "compress/gzip".


Installation
====
```go get github.com/klauspost/pgzip```

Usage
====

To use as a replacement for gzip, exchange import ```compress/gzip``` with ```import gzip github.com/klauspost/pgzip```. The API should be compatible.

To change the block size, use the added (*pgzip.Writer).SetConcurrency(blockSize, blocks int) function. With this you can control the approximate size of your blocks, as well as how many you want to be processing in parallel. Default values for this is SetConcurrency(250000, 15).


Example:
```
var b bytes.Buffer
w := gzip.NewWriter(&b)
w.SetConcurrency(100000, 10)
w.Write([]byte("hello, world\n"))
w.Close()
```

To get any performance gains, you should at least be compressing more than 100KB of data.


Performance
====
Compression cost is usually about 0.2% with default settings with a block size of 250k.

Example with GOMAXPROC set to 4 (dual core with 2 hyperthreads)

Compressor  | MB/sec   | speedup | size | overhead
------------|----------|---------|------|---------
gzip (golang) | 15.082MB/s | 1.0x | 6.405.193 | 0%
pgzip (golang) |  25.816MB/s|1.7x | 6.421.585 | 0.2%
