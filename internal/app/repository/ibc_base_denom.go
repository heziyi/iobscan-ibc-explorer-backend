package repository

import (
	"context"

	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/entity"
	"github.com/qiniu/qmgo"
	"go.mongodb.org/mongo-driver/bson"
)

type IBaseDenomRepo interface {
	FindAll() (entity.IBCBaseDenomList, error)
	FindBySymbol(symbol string) (entity.IBCBaseDenom, error)
}

var _ IBaseDenomRepo = new(BaseDenomRepo)

type BaseDenomRepo struct {
}

func (repo *BaseDenomRepo) coll() *qmgo.Collection {
	return mgo.Database(ibcDatabase).Collection(entity.IBCBaseDenom{}.CollectionName())
}

func (repo *BaseDenomRepo) FindAll() (entity.IBCBaseDenomList, error) {
	var res entity.IBCBaseDenomList
	err := repo.coll().Find(context.Background(), bson.M{}).All(&res)
	return res, err
}

func (repo *BaseDenomRepo) FindBySymbol(symbol string) (entity.IBCBaseDenom, error) {
	var res entity.IBCBaseDenom
	err := repo.coll().Find(context.Background(), bson.M{"symbol": symbol}).One(&res)
	return res, err
}
