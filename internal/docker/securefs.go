package docker

import "os"

// A pinned directory keeps subsequent file operations on the verified inode.
type secureDir struct{ *os.File }
