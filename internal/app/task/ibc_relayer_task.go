package task

/***
  ibc_relayer_task 定时任务
  功能范围：
      1.根据已注册的relayer的地址、链信息，更新channel_pair_info字段。
      2.更新relayer的update_time。
      3.更新channel页面relayer的数量、channel的更新时间、chain页面relayer数量。
      4.增量更新(包括已注册,未注册)relayer相关信息(交易总数、成功交易总数、relayer费用总价值、交易总价值)。
*/
import (
	"fmt"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/constant"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/repository"
	"go.mongodb.org/mongo-driver/mongo"
	"math"
	"sync"
	"time"

	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/dto"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/model/entity"
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/utils"
	"github.com/qiniu/qmgo"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

type IbcRelayerCronTask struct {
	chainConfigMap map[string]*entity.ChainConfig
	//key:relayer_id
	relayerTxsDataMap map[string]TxsAmtItem
	//key:address+Chain+Channel
	relayerValueMap map[string]decimal.Decimal
	//key: BaseDenom+ChainId
	denomPriceMap        map[string]CoinItem
	channelUpdateTimeMap *sync.Map
}
type (
	TxsAmtItem struct {
		Txs        int64
		TxsSuccess int64
		Denom      string
		ChainId    string
		Amt        decimal.Decimal
	}

	CoinItem struct {
		Price float64
		Scale int
	}
)

func (t *IbcRelayerCronTask) Name() string {
	return "ibc_relayer_task"
}
func (t *IbcRelayerCronTask) Cron() int {
	if taskConf.CronTimeRelayerTask > 0 {
		return taskConf.CronTimeRelayerTask
	}
	return ThreeMinute
}

func (t *IbcRelayerCronTask) Run() int {
	if err := t.init(); err != nil {
		return -1
	}

	t.denomPriceMap = getTokenPriceMap()
	doRegisterRelayer(t.denomPriceMap)
	_ = t.todayStatistics()
	_ = t.yesterdayStatistics()
	t.CheckAndChangeRelayer()
	//最后更新chains,channels信息
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		t.updateIbcChainsRelayer()
	}()
	go func() {
		defer wg.Done()
		t.updateIbcChannelRelayer()
	}()
	wg.Wait()

	return 1
}

func (t *IbcRelayerCronTask) init() error {
	if chainConfigMap, err := getAllChainMap(); err != nil {
		logrus.Errorf("task %s getAllChainMap err, %v", t.Name(), err)
		return err
	} else {
		t.chainConfigMap = chainConfigMap
	}

	t.channelUpdateTimeMap = new(sync.Map)
	return nil
}

func (t *IbcRelayerCronTask) updateRelayerUpdateTime(relayer *entity.IBCRelayerNew) {
	//get latest update_client time
	updateTime := t.getUpdateTime(relayer)
	if relayer.UpdateTime >= updateTime {
		return
	}
	if err := relayerRepo.UpdateRelayerTime(relayer.RelayerId, updateTime); err != nil {
		logrus.Error("update relayer about update_time fail, ", err.Error())
	}
}
func (t *IbcRelayerCronTask) CheckAndChangeRelayer() {
	//并发处理relayer信息
	handleRelayers := func(workNum int, relayers []*entity.IBCRelayerNew, dowork func(one *entity.IBCRelayerNew)) {
		var wg sync.WaitGroup
		wg.Add(workNum)
		for i := 0; i < workNum; i++ {
			num := i
			go func(num int) {
				defer wg.Done()
				for id, v := range relayers {
					if id%workNum != num {
						continue
					}
					dowork(v)
				}
			}(num)
		}
		wg.Wait()
	}

	skip := int64(0)
	limit := int64(1000)
	for {
		relayers, err := relayerRepo.FindAll(skip, limit, repository.RelayerAllType)
		if err != nil {
			logrus.Error("find relayer by page fail, ", err.Error())
			return
		}
		handleRelayers(5, relayers, t.updateOneRelayerUpdateTime)

		if len(relayers) < int(limit) {
			break
		}
		skip += limit
	}
}

func (t *IbcRelayerCronTask) updateOneRelayerUpdateTime(one *entity.IBCRelayerNew) {
	//更新relayer的updateTime
	t.updateRelayerUpdateTime(one)
	//更新channel的updateTime
	for _, channelPair := range one.ChannelPairInfo {
		channelId := generateChannelId(channelPair.ChainA, channelPair.ChannelA, channelPair.ChainB, channelPair.ChannelB)
		t.updateIbcChannelRelayerInfo(channelId)
	}
}

func getTokenPriceMap() map[string]CoinItem {
	coinIdPriceMap, _ := tokenPriceRepo.GetAll()
	baseDenoms, err := baseDenomCache.FindAll()
	if err != nil {
		logrus.Error("find base_denom fail, ", err.Error())
		return nil
	}
	if len(coinIdPriceMap) == 0 {
		return nil
	}
	denomPriceMap := make(map[string]CoinItem, len(baseDenoms))
	for _, val := range baseDenoms {
		if price, ok := coinIdPriceMap[val.CoinId]; ok {
			denomPriceMap[val.Denom+val.ChainId] = CoinItem{Price: price, Scale: val.Scale}
		}
	}
	return denomPriceMap
}

func (t *IbcRelayerCronTask) updateIbcChannelRelayerInfo(channelId string) {
	if channelId != "" {
		value, ok := t.channelUpdateTimeMap.Load(channelId)
		if ok && value.(int64) > 0 {
			if err := channelRepo.UpdateOneUpdateTime(channelId, value.(int64)); err != nil && err != mongo.ErrNoDocuments {
				logrus.Error("update ibc_channel updateTime fail, ", err.Error())
			}
		}

	}
}

func getRelayerAddrAndChains(channelPairInfo []entity.ChannelPairInfo) (addrs []string, chains []string) {
	addrs = make([]string, 0, len(channelPairInfo))
	chains = make([]string, 0, len(channelPairInfo))
	for i := range channelPairInfo {
		if len(channelPairInfo[i].ChainAAddress) > 0 {
			addrs = append(addrs, channelPairInfo[i].ChainAAddress)
		}
		if len(channelPairInfo[i].ChainBAddress) > 0 {
			addrs = append(addrs, channelPairInfo[i].ChainBAddress)
		}
		chains = append(chains, channelPairInfo[i].ChainA, channelPairInfo[i].ChainB)
	}
	addrs = utils.DistinctSliceStr(addrs)
	chains = utils.DistinctSliceStr(chains)
	return
}

//获取每个relayer的txs、txs_success、amount
func AggrRelayerTxsAndAmt(relayerNew *entity.IBCRelayerNew) map[string]TxsAmtItem {
	addrs, chains := getRelayerAddrAndChains(relayerNew.ChannelPairInfo)
	res, err := relayerDenomStatisticsRepo.CountRelayerBaseDenomAmtAndTxs(addrs, chains)
	if err != nil {
		logrus.Error("aggregate relayer txs have fail, ", err.Error(),
			" relayer_id: ", relayerNew.RelayerId,
			" relayer_name: ", relayerNew.RelayerName)
		return nil
	}
	relayerTxsAmtMap := make(map[string]TxsAmtItem, 20)
	for _, item := range res {
		key := fmt.Sprintf("%s%s", item.BaseDenom, item.BaseDenomChainId)
		value, exist := relayerTxsAmtMap[key]
		if exist {
			value.Txs += item.TotalTxs
			value.Amt = value.Amt.Add(decimal.NewFromFloat(item.Amount))
			if item.TxStatus == int(entity.TxStatusSuccess) {
				value.TxsSuccess += item.TotalTxs
			}
			relayerTxsAmtMap[key] = value
		} else {
			data := TxsAmtItem{
				ChainId: item.BaseDenomChainId,
				Denom:   item.BaseDenom,
				Txs:     item.TotalTxs,
				Amt:     decimal.NewFromFloat(item.Amount),
			}
			if item.TxStatus == int(entity.TxStatusSuccess) {
				data.TxsSuccess = item.TotalTxs
			}
			relayerTxsAmtMap[key] = data
		}
	}
	return relayerTxsAmtMap
}

func AggrRelayerFeeAmt(relayerNew *entity.IBCRelayerNew) map[string]TxsAmtItem {
	addrs, chains := getRelayerAddrAndChains(relayerNew.ChannelPairInfo)
	res, err := relayerFeeStatisticsRepo.CountRelayerFeeDenomAmt(addrs, chains)
	if err != nil {
		logrus.Error("aggregate relayer txs have fail, ", err.Error(),
			" relayer_id: ", relayerNew.RelayerId,
			" relayer_name: ", relayerNew.RelayerName)
		return nil
	}
	relayerTxsAmtMap := make(map[string]TxsAmtItem, 20)
	for _, item := range res {
		key := fmt.Sprintf("%s%s", item.FeeDenom, item.ChainId)
		value, exist := relayerTxsAmtMap[key]
		if exist {
			value.Amt = value.Amt.Add(decimal.NewFromFloat(item.Amount))
			relayerTxsAmtMap[key] = value
		} else {
			data := TxsAmtItem{
				ChainId: item.ChainId,
				Denom:   item.FeeDenom,
				Amt:     decimal.NewFromFloat(item.Amount),
			}
			relayerTxsAmtMap[key] = data
		}
	}
	return relayerTxsAmtMap
}

//dependence: AggrRelayerFeeAmt or AggrRelayerTxsAndAmt
func caculateRelayerTotalValue(denomPriceMap map[string]CoinItem, relayerTxsDataMap map[string]TxsAmtItem) decimal.Decimal {
	totalValue := decimal.NewFromFloat(0)
	for _, data := range relayerTxsDataMap {
		baseDenomValue := decimal.NewFromFloat(0)
		decAmt := data.Amt
		if coin, ok := denomPriceMap[data.Denom+data.ChainId]; ok {
			if coin.Scale > 0 {
				baseDenomValue = decAmt.Div(decimal.NewFromFloat(math.Pow10(coin.Scale))).Mul(decimal.NewFromFloat(coin.Price))
			}
		}
		totalValue = totalValue.Add(baseDenomValue)
	}
	return totalValue
}

func (t *IbcRelayerCronTask) getChannelsStatus(chainId, dcChainId string) []*entity.ChannelPath {
	cfg, ok := t.chainConfigMap[chainId]
	if ok {
		for _, v := range cfg.IbcInfo {
			if v.ChainId == dcChainId {
				return v.Paths
			}
		}
	}

	return nil
}

func (t *IbcRelayerCronTask) updateIbcChainsRelayer() {
	res, err := chainCache.FindAll()
	if err != nil {
		logrus.Error("find ibc_chains data fail, ", err.Error())
		return
	}
	for _, val := range res {
		relayerCnt, err := relayerRepo.CountChainRelayers(val.ChainId)
		if err != nil {
			logrus.Error("count relayers of chain fail, ", err.Error())
			continue
		}
		if err := chainRepo.UpdateRelayers(val.ChainId, relayerCnt); err != nil {
			logrus.Error("update ibc_chain relayers fail, ", err.Error())
		}
	}
	return
}

func (t *IbcRelayerCronTask) updateIbcChannelRelayer() {
	res, err := channelRepo.FindAll()
	if err != nil {
		logrus.Error("find ibc_channel data fail, ", err.Error())
		return
	}
	for _, val := range res {
		relayerCnt, err := relayerRepo.CountChannelRelayers(val.ChainA, val.ChannelA, val.ChainB, val.ChannelB)
		if err != nil {
			logrus.Error("count relayers of channel fail, ", err.Error())
			continue
		}
		if err := channelRepo.UpdateRelayers(val.ChannelId, relayerCnt); err != nil {
			logrus.Error("update ibc_channel relayers fail, ", err.Error())
		}
	}
	return
}

//1: updateTime
func (t *IbcRelayerCronTask) getUpdateTime(relayer *entity.IBCRelayerNew) int64 {
	var startTime int64

	//use unbonding_time
	startTime = time.Now().Add(-24 * 21 * time.Hour).Unix()
	if relayer.UpdateTime > 0 && relayer.UpdateTime <= startTime {
		startTime = relayer.UpdateTime
		//} else {
		//	unbondTime, _ := unbondTimeCache.GetUnbondTime(relayer.ChainA)
		//	if unbondTime != "" {
		//		if unbondTimeSeconds, err := strconv.ParseInt(strings.ReplaceAll(unbondTime, "s", ""), 10, 64); err == nil && unbondTimeSeconds > 0 && unbondTimeSeconds < startTime {
		//			startTime = time.Now().Add(time.Duration(-unbondTimeSeconds) * time.Second).Unix()
		//		}
		//	}
	}

	getChannelPairUpdateTime := func(channelPair entity.ChannelPairInfo) (int64, string) {
		var updateTimeA, updateTimeB int64
		var clientIdA, clientIdB string
		var err error
		group := sync.WaitGroup{}
		group.Add(2)
		go func() {
			defer group.Done()
			clientIdA, err = t.getChannelClient(channelPair.ChainA, channelPair.ChannelA)
			if err != nil {
				logrus.Warnf("get channel client fail, %s", err.Error())
				return
			}
			updateTimeA, err = txRepo.GetUpdateTimeByUpdateClient(channelPair.ChainA, channelPair.ChainAAddress, clientIdA, startTime)
			if err != nil {
				logrus.Warnf("get channel pairInfo updateTime fail, %s", err.Error())
			}
		}()

		go func() {
			defer group.Done()
			clientIdB, err = t.getChannelClient(channelPair.ChainB, channelPair.ChannelB)
			if err != nil {
				logrus.Warnf("get channel client fail, %s", err.Error())
				return
			}
			updateTimeB, err = txRepo.GetUpdateTimeByUpdateClient(channelPair.ChainB, channelPair.ChainBAddress, clientIdB, startTime)
			if err != nil {
				logrus.Warnf("get channel pairInfo updateTime fail, %s", err.Error())
			}
		}()
		group.Wait()
		channelId := generateChannelId(channelPair.ChainA, channelPair.ChannelA, channelPair.ChainB, channelPair.ChannelB)

		if updateTimeA >= updateTimeB {
			return updateTimeA, channelId
		}
		return updateTimeB, channelId
	}

	//并发处理获取最新的updateTime
	dochannelPairInfos := func(workNum int, pairInfos []entity.ChannelPairInfo, dowork func(one entity.ChannelPairInfo, pos int)) {
		var wg sync.WaitGroup
		wg.Add(workNum)
		for i := 0; i < workNum; i++ {
			num := i
			go func(num int) {
				defer wg.Done()
				for id, v := range pairInfos {
					if id%workNum != num {
						continue
					}
					dowork(v, id)
				}
			}(num)
		}
		wg.Wait()
	}

	updateTimes := make([]int64, len(relayer.ChannelPairInfo))
	dochannelPairInfos(3, relayer.ChannelPairInfo, func(one entity.ChannelPairInfo, pos int) {
		updateTime, channelId := getChannelPairUpdateTime(one)
		t.channelUpdateTimeMap.Store(channelId, updateTime)
		updateTimes[pos] = updateTime
	})
	var relayerUpdateTime int64
	for i := range updateTimes {
		if updateTimes[i] > relayerUpdateTime {
			relayerUpdateTime = updateTimes[i]
		}
	}

	return relayerUpdateTime
}

func (t *IbcRelayerCronTask) getChannelClient(chainId, channelId string) (string, error) {
	chainConf, ok := t.chainConfigMap[chainId]
	if !ok {
		return "", fmt.Errorf("%s config not found", chainId)
	}

	port := chainConf.GetPortId(channelId)
	state, err := queryClientState(chainConf.Lcd, chainConf.LcdApiPath.ClientStatePath, port, channelId)
	if err != nil {
		return "", err
	}

	return state.IdentifiedClientState.ClientId, nil
}

func (t *IbcRelayerCronTask) todayStatistics() error {
	logrus.Infof("task %s exec today statistics", t.Name())
	startTime, endTime := todayUnix()
	segments := []*segment{
		{
			StartTime: startTime,
			EndTime:   endTime,
		},
	}
	if err := relayerStatisticsTask.RunIncrement(segments[0]); err != nil {
		logrus.Errorf("task %s todayStatistics error, %v", t.Name(), err)
		return err
	}
	relayerDataTask.handleNewRelayerOnce(segments, false)

	return nil
}

func (t *IbcRelayerCronTask) yesterdayStatistics() error {
	mmdd := time.Now().Format(constant.TimeFormatMMDD)
	incr, _ := statisticsCheckRepo.GetIncr(t.Name(), mmdd)
	if incr > statisticsCheckTimes {
		return nil
	}

	logrus.Infof("task %s check yeaterday statistics, time: %d", t.Name(), incr)
	startTime, endTime := yesterdayUnix()
	segments := []*segment{
		{
			StartTime: startTime,
			EndTime:   endTime,
		},
	}
	if err := relayerStatisticsTask.RunIncrement(segments[0]); err != nil {
		logrus.Errorf("task %s todayStatistics error, %v", t.Name(), err)
		return err
	}
	relayerDataTask.handleNewRelayerOnce(segments, false)

	_ = statisticsCheckRepo.Incr(t.Name(), mmdd)
	return nil
}

//func checkAndUpdateRelayerSrcChainAddr() {
//	skip := int64(0)
//	limit := int64(500)
//	startTimeA := time.Now().Unix()
//	for {
//		relayers, err := relayerRepo.FindEmptyAddrAll(skip, limit)
//		if err != nil {
//			logrus.Error("find relayer by page fail, ", err.Error())
//			return
//		}
//		for _, relayer := range relayers {
//			for i, val := range relayer.ChannelPairInfo {
//				if val.ChainAAddress == "" {
//					addrs := getChannalPairInfo(val)
//					if len(addrs) > 0 {
//						addrs = utils.DistinctSliceStr(addrs)
//						val.ChainAAddress = addrs[0]
//					}
//				}
//				relayer.ChannelPairInfo[i] = val
//			}
//			if err := relayerRepo.UpdateChannelPairInfo(relayer.RelayerId, relayer.ChannelPairInfo); err != nil && !qmgo.IsDup(err) {
//				logrus.Error("Update Src Address failed, " + err.Error())
//			}
//		}
//		if len(relayers) < int(limit) {
//			break
//		}
//		skip += limit
//	}
//	logrus.Infof("cronjob execute checkAndUpdateRelayerSrcChainAddr finished, time use %d(s)", time.Now().Unix()-startTimeA)
//}

//根据目标地址反查发起地址
func getChannalPairInfo(pair entity.ChannelPairInfo) []string {
	var (
		addrs, historyAddrs []string
	)
	addrs = getSrcChainAddress(&dto.GetRelayerInfoDTO{
		ScChainId:      pair.ChainA,
		ScChannel:      pair.ChannelA,
		DcChainId:      pair.ChainB,
		DcChannel:      pair.ChannelB,
		DcChainAddress: pair.ChainBAddress,
	}, false)
	if len(addrs) > 0 {
		addrs = utils.DistinctSliceStr(addrs)
		return addrs
	}

	historyAddrs = getSrcChainAddress(&dto.GetRelayerInfoDTO{
		ScChainId:      pair.ChainA,
		ScChannel:      pair.ChannelA,
		DcChainId:      pair.ChainB,
		DcChannel:      pair.ChannelB,
		DcChainAddress: pair.ChainBAddress,
	}, true)

	if len(historyAddrs) > 0 {
		historyAddrs = utils.DistinctSliceStr(historyAddrs)
	}
	return addrs
}
func getSrcChainAddress(info *dto.GetRelayerInfoDTO, historyData bool) []string {
	//查询relayer在原链所有地址
	var (
		chainAAddress []string
		msgPacketId   string
	)

	if historyData {
		ibcTx, err := ibcTxRepo.GetHistoryOneRelayerScTxPacketId(info)
		if err == nil {
			msgPacketId = ibcTx.ScTxInfo.Msg.CommonMsg().PacketId
		}
	} else {
		ibcTx, err := ibcTxRepo.GetOneRelayerScTxPacketId(info)
		if err == nil {
			msgPacketId = ibcTx.ScTxInfo.Msg.CommonMsg().PacketId
		}
	}
	if msgPacketId != "" {
		scAddr, err := txRepo.GetRelayerScChainAddr(msgPacketId, info.ScChainId)
		if err != nil && err != qmgo.ErrNoSuchDocuments {
			logrus.Errorf("get srAddr relayer packetId fail, %s", err.Error())
		}
		if scAddr != "" {
			chainAAddress = append(chainAAddress, scAddr)
		}
	}
	return chainAAddress
}
