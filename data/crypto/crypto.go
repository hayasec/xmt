package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"io"

	"github.com/iDigitalFlame/xmt/data"
)

type reader struct {
	r io.Reader
	c Reader
}
type writer struct {
	w io.Writer
	c Writer
}

// Reader is an interface that supports reading bytes from a Reader through the specified Cipher.
type Reader interface {
	Read(io.Reader, []byte) (int, error)
}

// Writer is an interface that supports writing bytes to a Writer through the specified Cipher.
type Writer interface {
	Flush(io.Writer) error
	Write(io.Writer, []byte) (int, error)
}

func (w *writer) Flush() error {
	if err := w.c.Flush(w.w); err != nil {
		return err
	}
	if f, ok := w.w.(data.Flusher); ok {
		return f.Flush()
	}
	return nil
}
func (w *writer) Close() error {
	if err := w.Flush(); err != nil {
		return err
	}
	if c, ok := w.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// NewDes attempts to create a new DES block Cipher from the provided key data. Errors will be returned
// if the key length is invalid.
func NewDes(k []byte) (cipher.Block, error) {
	return des.NewCipher(k)
}

// NewAes attempts to create a new AES block Cipher from the provided key data. Errors will be returned
// if the key length is invalid.
func NewAes(k []byte) (cipher.Block, error) {
	return aes.NewCipher(k)
}
func (r *reader) Read(b []byte) (int, error) {
	return r.c.Read(r.r, b)
}
func (w *writer) Write(b []byte) (int, error) {
	return w.c.Write(w.w, b)
}

// NewReader creates a data.Reader type from the specified Cipher and Reader.
func NewReader(c Reader, r io.Reader) data.Reader {
	if c == nil {
		return data.NewReader(r)
	}
	return data.NewReader(&reader{c: c, r: r})
}

// NewWriter creates a data.Writer type from the specified Cipher and Writer.
func NewWriter(c Writer, w io.Writer) data.Writer {
	if c == nil {
		return data.NewWriter(w)
	}
	return data.NewWriter(&writer{c: c, w: w})
}

// NewTrippleDes attempts to create a new Tripple DES block Cipher from the provided key data. Errors
// will be returned if the key length is invalid.
func NewTrippleDes(k []byte) (cipher.Block, error) {
	return des.NewTripleDESCipher(k)
}

// BlockDecryptReader creates a data.Reader type from the specified block Cipher, IV and Reader.
// This is used to Decrypt data.
func BlockDecryptReader(b cipher.Block, iv []byte, r io.Reader) data.Reader {
	return data.NewReader(&cipher.StreamReader{
		R: r,
		S: cipher.NewCFBDecrypter(b, iv),
	})
}

// BlockDecryptWriter creates a data.Writer type from the specified block Cipher, IV and Writer.
// This is used to Decrypt data.
func BlockDecryptWriter(b cipher.Block, iv []byte, w io.Writer) data.Writer {
	return data.NewWriter(&cipher.StreamWriter{
		W: w,
		S: cipher.NewCFBDecrypter(b, iv),
	})
}

// BlockEncryptReader creates a data.Reader type from the specified block Cipher, IV and Reader.
// This is used to Encrypt data.
func BlockEncryptReader(b cipher.Block, iv []byte, r io.Reader) data.Reader {
	return data.NewReader(&cipher.StreamReader{
		R: r,
		S: cipher.NewCFBEncrypter(b, iv),
	})
}

// BlockEncryptWriter creates a data.Reader type from the specified block Cipher, IV and Writer.
// This is used to Encrypt data.
func BlockEncryptWriter(b cipher.Block, iv []byte, w io.Writer) data.Writer {
	return data.NewWriter(&cipher.StreamWriter{
		W: w,
		S: cipher.NewCFBEncrypter(b, iv),
	})
}