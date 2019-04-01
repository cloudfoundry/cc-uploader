CC Uploader
===========

**Note**: This repository should be imported as `code.cloudfoundry.org/cc-uploader`.

CC Bridge component to enable Diego to upload files to Cloud Controller's blobstore

## Uploading Droplets & Build Artifacts

Uploading droplets & build artifacts via CC involves crafting a correctly-formed multipart request. For Droplets we also poll until the async job completes.

## Testing

To specify a remote cloud controller to test against, use the following environment variables:

CC_ADDRESS the hostname for a deployed CC
CC_USERNAME, CC_PASSWORD the basic auth credentials for the droplet upload endpoint
CC_APPGUID a valid app guid on that deployed CC

####Learn more about Diego and its components at [diego-design-notes](https://github.com/cloudfoundry-incubator/diego-design-notes)


## Generating cert fixtures

```sh
echo "Generating CA"
certstrap --depot-path . init --passphrase '' --common-name cc_uploader_ca_cn
echo "Generating server csr"
certstrap --depot-path . request-cert --passphrase '' --common-name cc_cn --ip 127.0.0.1
echo "Generating server cert"
certstrap --depot-path . sign cc_cn --CA cc_uploader_ca_cn
echo "Generating client csr"
certstrap --depot-path . request-cert --passphrase '' --common-name cc_uploader_cn --ip 127.0.0.1
echo "Generating client cert"
certstrap --depot-path . sign cc_uploader_cn --CA cc_uploader_ca_cn
```

and once you've generated those,

```sh
cd certs/
cp ../cc_uploader_ca_cn.crt ca.crt
certstrap --depot-path . request-cert --passphrase '' --domain '*.localhost,localhost' --ip 127.0.0.1
mv \*.localhost.csr server.csr
mv \*.localhost.key server.key
certstrap --depot-path . sign server --CA ca
certstrap --depot-path . request-cert --passphrase '' --common-name client --ip 127.0.0.1
certstrap --depot-path . sign client --CA ca
```
