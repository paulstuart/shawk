#!/bin/bash

set -eux

sudo apt-get update
sudo apt-get install -y --no-install-recommends  build-essential gcc make git

# Install Go
GOVER='1.14.1'
GOTAR="go${GOVER}.linux-amd64.tar.gz"

wget https://dl.google.com/go/${GOTAR}
sudo tar -C /usr/local -xzf ${GOTAR}
rm -f ${GOTAR}
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc

mkdir -p /home/vagrant/go/src/github.com/yuuki/shawk/

# Install postgres
PG_VERSION=11

wget --quiet -O - https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo apt-key add -
sudo bash -c "echo 'deb http://apt.postgresql.org/pub/repos/apt/ bionic-pgdg main' > /etc/apt/sources.list.d/pgdg.list"
sudo apt-get update
sudo apt-get install -y --no-install-recommends postgresql-${PG_VERSION} postgresql-client-${PG_VERSION} postgresql-contrib-${PG_VERSION}

PG_USER='shawk'
PG_PASSWD='shawk'
PG_DB='shawk'
sudo -u postgres psql -c "create role ${PG_USER} with login password '${PG_PASSWD}';"
sudo -u postgres createdb --owner ${PG_USER} ${PG_DB} --echo

echo 'Completed to setup'