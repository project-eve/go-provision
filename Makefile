#
# Makefile for go-provision
#

PKGNAME   := zededa-provision
MAJOR_VER := 1
MINOR_VER := 0
ARCH        ?= amd64

BUILD_DATE  := $(shell date +"%Y-%m-%d %H:%M %Z")
GIT_VERSION := $(shell git describe --match v --abbrev=8 --always --dirty)
BRANCH_NAME := $(shell git rev-parse --abbrev-ref HEAD)
VERSION     := $(MAJOR_VER).$(MINOR_VER)-$(GIT_VERSION)

# For future use
#LDFLAGS     := -ldflags "-X=main.Version=$(VERSION) -X=main.Build=$(BUILD_DATE)"

PKG         := $(PKGNAME)-$(VERSION)-$(BRANCH_NAME)
OBJDIR      := $(PWD)/obj/$(ARCH)
PKGDIR      := $(OBJDIR)/$(PKG)/opt/zededa
BINDIR      := $(PKGDIR)/bin
ETCDIR      := $(PKGDIR)/etc/zededa

APPS = \
	downloader 	\
	verifier 	\
	client 		\
	server 		\
	register 	\
	zedrouter 	\
	xenmgr 		\
	identitymgr	\
	zedmanager 	\
	eidregister

.PHONY: all clean pkg obj install

all: pkg

pkg: obj build
	@cp -p README $(ETCDIR)
	@cp -p etc/* $(ETCDIR)
	@cp -p scripts/*.sh $(BINDIR)
	@mkdir -p $(OBJDIR)/$(PKG)/DEBIAN
	@sed "s/__VERSION__/$(VERSION)/;s/__ARCH__/$(ARCH)/" package/control > $(OBJDIR)/$(PKG)/DEBIAN/control
	@cd $(OBJDIR) && dpkg-deb --build $(PKG)

obj:
	@mkdir -p $(BINDIR) $(ETCDIR)

build:
	@for app in $(APPS); do \
		echo $$app; \
	       	CGO_ENABLED=0 \
		GOOS=linux \
		GOARCH=$(ARCH) go build \
			-o $(BINDIR)/$$app github.com/zededa/go-provision/$$app; \
	done

clean:
	@rm -rf obj
