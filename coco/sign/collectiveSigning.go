package sign

import (
	"errors"
	"io"
	"strconv"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/dedis/crypto/abstract"
	"github.com/dedis/prifi/coco/coconet"
	"github.com/dedis/prifi/coco/hashid"
	// "strconv"
	// "os"
)

// Collective Signing via ElGamal
// 1. Announcement
// 2. Commitment
// 3. Challenge
// 4. Response

var ErrUnknownMessageType error = errors.New("Received message of unknown type")

// Start listening for messages coming from parent(up)
func (sn *Node) Listen() error {
	sn.setPool()
	go sn.get()
	return nil
}

// Get multiplexes all messages from TCPHost using application logic
func (sn *Node) get() {
	sn.UpdateTimeout()
	msgchan, errchan := sn.Host.Get()
	for {
		nm := <-msgchan
		err := <-errchan
		if err != nil {
			if err == coconet.ErrNotEstablished {
				continue
			}

			log.Warnf("signing node: error getting: %v", err)
			if err == io.EOF {
				sn.closed <- err
				return
			}
			if err == coconet.ErrorConnClosed {
				continue
			}
		}

		// interpret network message as Siging Message
		sm := nm.Data.(*SigningMessage)
		sm.From = nm.From
		go func(sm *SigningMessage) {
			switch sm.Type {
			// if it is a bad message just ignore it
			default:
				return

			// if it is an announcement or challenge it should be from parent
			// Change views after a set number of rounds per view.
			// This allows for deterministic testing and more practical test setup.
			// Should the view-change be broadcast, or should we just trust (for now)
			// that if we recieve a view change request, it is coming (honestly) from
			// our new view's parent? This simplification might be easier to start with.
			// TODO: this could be expanded fairly easily to use Announcement messages
			// as leader "heart-beats" and when the leader is deemed dead we could also
			// announce a view change.
			case Announcement:
				if !sn.IsParent(sm.View, sm.From) {
					log.Fatalln("received announcement from non-parent")
					return
				}
				if err := sn.Announce(sm.View, sm.Am); err != nil {
					log.Errorln("announce error:", err)
				}

			case Challenge:
				if !sn.IsParent(sm.View, sm.From) {
					log.Fatalln("received announcement from non-parent")
					return
				}

				if err := sn.Challenge(sm.View, sm.Chm); err != nil {
					log.Errorln("challenge error:", err)
				}

			// if it is a commitment or response it is from the child
			case Commitment:
				if !sn.IsChild(sm.View, sm.From) {
					log.Fatalln("received announcement from non-parent")
					return
				}

				// shove message on commit channel for its round
				round := sm.Com.Round
				sn.roundLock.Lock()
				comch := sn.ComCh[round]
				sn.roundLock.Unlock()
				comch <- sm
			case Response:
				if !sn.IsChild(sm.View, sm.From) {
					log.Fatalln("received announcement from non-parent")
					return
				}

				// shove message on response channel for its round
				round := sm.Rm.Round
				sn.roundLock.Lock()
				rmch := sn.RmCh[round]
				sn.roundLock.Unlock()
				rmch <- sm
			case ViewChange:
				view := sm.Vc.ViewNo
				parent := sm.From
				peers := sn.Peers()
				children := make([]string, 0, len(peers)-1)
				for p := range peers {
					if p == parent {
						continue
					}
					children = append(children, p)
				}
				sn.NewView(view, parent, children)
				messgs := make([]coconet.BinaryMarshaler, sn.NChildren(view))
				for i := range messgs {
					messgs[i] = sm
				}
				if err := sn.PutDown(view, messgs); err != nil {
					log.Errorln("failed to putdown ViewChange announcement")
				}
			case Error:
				log.Println("Received Error Message:", ErrUnknownMessageType, sm, sm.Err)
			}
		}(sm)
	}

}

// Block until a message from a child is received via network
func (sn *Node) waitDownOnNM(ch chan coconet.NetworkMessg, errch chan error) (
	coconet.NetworkMessg, error) {
	nm := <-ch
	err := <-errch

	return nm, err
}

// Used in Commit and Respond when waiting for children Commits and Responses
// The commits and responses are read from the commit and respond channel
// as they are put there by the getDown function
func (sn *Node) waitOn(view int, ch chan *SigningMessage, timeout time.Duration, what string) []*SigningMessage {
	nChildren := sn.NChildren(view)
	messgs := make([]*SigningMessage, 0)
	received := 0
	if nChildren > 0 {
		for {
			select {
			case sm := <-ch:
				messgs = append(messgs, sm)
				received += 1
				if received == nChildren {
					return messgs
				}
			case <-time.After(timeout):
				log.Warnln(sn.Name(), "timeouted on", what, timeout, "got", len(messgs), "out of", nChildren)
				return messgs
			}
		}
	}

	return messgs
}

func (sn *Node) Announce(view int, am *AnnouncementMessage) error {
	// set up commit and response channels for the new round
	Round := am.Round
	sn.roundLock.Lock()
	sn.Rounds[Round] = NewRound()
	sn.ComCh[Round] = make(chan *SigningMessage, sn.NChildren(view))
	sn.RmCh[Round] = make(chan *SigningMessage, sn.NChildren(view))
	sn.roundLock.Unlock()

	// the root is the only node that keeps track of round # internally
	if sn.IsRoot(view) {
		// sequential round number
		sn.Round = Round
		sn.LastSeenRound = Round

		// Create my back link to previous round
		sn.SetBackLink(Round)
		// sn.SetAccountableRound(Round)
	}

	// doing this before annoucing children to avoid major drama
	if !sn.IsRoot(view) && sn.ShouldIFail("commit") {
		log.Warn(sn.Name(), "not commiting for round", Round)
		return nil
	}

	// Inform all children of announcement
	messgs := make([]coconet.BinaryMarshaler, sn.NChildren(view))
	for i := range messgs {
		sm := SigningMessage{Type: Announcement, View: view, Am: am}
		messgs[i] = &sm
	}
	if err := sn.PutDown(view, messgs); err != nil {
		return err
	}

	return sn.Commit(view, am.Round)
}

// Create round lasting secret and commit point v and V
// Initialize log structure for the round
func (sn *Node) initCommitCrypto(Round int) {
	round := sn.Rounds[Round]
	// generate secret and point commitment for this round
	rand := sn.suite.Cipher([]byte(sn.Name()))
	round.Log = SNLog{}
	round.Log.v = sn.suite.Secret().Pick(rand)
	round.Log.V = sn.suite.Point().Mul(nil, round.Log.v)
	// initialize product of point commitments
	round.Log.V_hat = sn.suite.Point().Null()
	sn.add(round.Log.V_hat, round.Log.V)

	round.X_hat = sn.suite.Point().Null()
	sn.add(round.X_hat, sn.PubKey)
}

func (sn *Node) Commit(view int, Round int) error {

	round := sn.Rounds[Round]
	sn.LastSeenRound = max(Round, sn.LastSeenRound)
	if round == nil {
		// was not announced of this round, should retreat
		return nil
	}

	sn.initCommitCrypto(Round)

	// wait on commits from children
	sn.UpdateTimeout()
	messgs := sn.waitOn(view, sn.ComCh[Round], sn.Timeout(), "commits")

	sn.roundLock.Lock()
	delete(sn.ComCh, Round)
	sn.roundLock.Unlock()

	// prepare to handle exceptions
	round.ExceptionList = make([]abstract.Point, 0)
	round.ChildV_hat = make(map[string]abstract.Point, len(sn.Children(view)))
	round.ChildX_hat = make(map[string]abstract.Point, len(sn.Children(view)))
	children := sn.Children(view)

	// Commits from children are the first Merkle Tree leaves for the round
	round.Leaves = make([]hashid.HashId, 0)
	round.LeavesFrom = make([]string, 0)

	for key := range children {
		round.ChildX_hat[key] = sn.suite.Point().Null()
		round.ChildV_hat[key] = sn.suite.Point().Null()
	}
	for _, sm := range messgs {
		from := sm.From
		switch sm.Type {
		default: // default == no response from i
			// fmt.Println(sn.Name(), "no commit from", i)
			round.ExceptionList = append(round.ExceptionList, children[from].PubKey())
			continue
		case Commitment:
			round.Leaves = append(round.Leaves, sm.Com.MTRoot)
			round.LeavesFrom = append(round.LeavesFrom, from)
			round.ChildV_hat[from] = sm.Com.V_hat
			round.ChildX_hat[from] = sm.Com.X_hat
			round.ExceptionList = append(round.ExceptionList, sm.Com.ExceptionList...)

			// add good child server to combined public key, and point commit
			sn.add(round.X_hat, sm.Com.X_hat)
			sn.add(round.Log.V_hat, sm.Com.V_hat)
		}

	}

	if sn.Type == PubKey {
		return sn.actOnCommits(view, Round)
	} else {
		sn.AddChildrenMerkleRoots(Round)
		sn.AddLocalMerkleRoot(view, Round)
		sn.HashLog(Round)
		sn.ComputeCombinedMerkleRoot(view, Round)
		return sn.actOnCommits(view, Round)
	}
}

// Finalize commits by initiating the challenge pahse if root
// Send own commitment message up to parent if non-root
func (sn *Node) actOnCommits(view, Round int) error {
	round := sn.Rounds[Round]
	var err error

	if sn.IsRoot(view) {
		sn.commitsDone <- Round
		err = sn.FinalizeCommits(view, Round)
	} else {
		// create and putup own commit message
		com := &CommitmentMessage{
			V:             round.Log.V,
			V_hat:         round.Log.V_hat,
			X_hat:         round.X_hat,
			MTRoot:        round.MTRoot,
			ExceptionList: round.ExceptionList,
			Round:         Round}

		err = sn.PutUp(view, &SigningMessage{
			View: view,
			Type: Commitment,
			Com:  com})
	}
	return err
}

// initiated by root, propagated by all others
func (sn *Node) Challenge(view int, chm *ChallengeMessage) error {
	round := sn.Rounds[chm.Round]
	sn.LastSeenRound = max(chm.Round, sn.LastSeenRound)
	if round == nil {
		return nil
	}
	// register challenge
	round.c = chm.C

	if sn.Type == PubKey {
		if err := sn.SendChildrenChallenges(view, chm); err != nil {
			return err
		}
		return sn.Respond(view, chm.Round)
	} else {
		// messages from clients, proofs computed
		if sn.CommitedFor(round) {
			if err := sn.SendLocalMerkleProof(view, chm); err != nil {
				return err
			}

		}
		if err := sn.SendChildrenChallengesProofs(view, chm); err != nil {
			return err
		}
		return sn.Respond(view, chm.Round)
	}

}

// Figure out which kids did not submit messages
// Add default messages to messgs, one per missing child
// as to make it easier to identify and add them to exception lists in one place
func (sn *Node) FillInWithDefaultMessages(view int, messgs []*SigningMessage) []*SigningMessage {
	children := sn.Children(view)

	allmessgs := make([]*SigningMessage, len(messgs))
	copy(allmessgs, messgs)

	for c := range children {
		found := false
		for _, m := range messgs {
			if m.From == c {
				found = true
				break
			}
		}

		if !found {
			allmessgs = append(allmessgs, &SigningMessage{Type: Default, View: view, From: c})
		}
	}

	return allmessgs
}

func (sn *Node) initResponseCrypto(Round int) {
	round := sn.Rounds[Round]
	// generate response   r = v - xc
	round.r = sn.suite.Secret()
	round.r.Mul(sn.PrivKey, round.c).Sub(round.Log.v, round.r)
	// initialize sum of children's responses
	round.r_hat = round.r
}

func (sn *Node) Respond(view, Round int) error {
	round := sn.Rounds[Round]
	sn.LastSeenRound = max(Round, sn.LastSeenRound)
	if round == nil || round.Log.v == nil {
		// If I was not announced of this round, or I failed to commit
		return nil
	}

	sn.LastSeenRound = max(Round, sn.LastSeenRound)
	sn.initResponseCrypto(Round)

	// wait on responses from children
	sn.UpdateTimeout()
	messgs := sn.waitOn(view, sn.RmCh[Round], sn.Timeout(), "responses")

	// initialize exception handling
	exceptionV_hat := sn.suite.Point().Null()
	exceptionX_hat := sn.suite.Point().Null()
	round.ExceptionList = make([]abstract.Point, 0)
	nullPoint := sn.suite.Point().Null()
	allmessgs := sn.FillInWithDefaultMessages(view, messgs)

	children := sn.Children(view)
	for _, sm := range allmessgs {
		from := sm.From
		switch sm.Type {
		default:
			// default == no response from child
			// log.Println(sn.Name(), "default in respose for child", from, sm)
			if children[from] != nil {
				round.ExceptionList = append(round.ExceptionList, children[from].PubKey())

				// remove public keys and point commits from subtree of faild child
				sn.add(exceptionX_hat, round.ChildX_hat[from])
				sn.add(exceptionV_hat, round.ChildV_hat[from])
			}
			continue
		case Response:
			// disregard response from children who did not commit
			_, ok := round.ChildV_hat[from]
			if ok == true && round.ChildV_hat[from].Equal(nullPoint) {
				continue
			}

			// log.Println(sn.Name(), "accepts response from", from, sm.Type)
			round.r_hat.Add(round.r_hat, sm.Rm.R_hat)

			sn.add(exceptionV_hat, sm.Rm.ExceptionV_hat)
			sn.add(exceptionX_hat, sm.Rm.ExceptionX_hat)
			round.ExceptionList = append(round.ExceptionList, sm.Rm.ExceptionList...)

		case Error:
			if sm.Err == nil {
				log.Errorln("Error message with no error")
				continue
			}

			// Report up non-networking error, probably signature failure
			log.Println(sn.Name(), "Error in respose for child", from, sm)
			err := errors.New(sm.Err.Err)
			sn.PutUpError(view, err)
			return err
		}
	}

	// remove exceptions from subtree that failed
	sn.sub(round.X_hat, exceptionX_hat)
	round.exceptionV_hat = exceptionV_hat

	return sn.actOnResponses(view, Round, exceptionV_hat, exceptionX_hat)
}

func (sn *Node) actOnResponses(view, Round int, exceptionV_hat abstract.Point, exceptionX_hat abstract.Point) error {
	round := sn.Rounds[Round]
	err := sn.VerifyResponses(view, Round)

	isroot := sn.IsRoot(view)
	// if error put it up if parent exists
	if err != nil && !isroot {
		sn.PutUpError(view, err)
		return err
	}

	// if no error send up own response
	if err == nil && !isroot {
		// if round.Log.v == nil && sn.ShouldIFail("response") {
		// 	return nil
		// }

		// create and putup own response message
		rm := &ResponseMessage{
			R_hat:          round.r_hat,
			ExceptionList:  round.ExceptionList,
			ExceptionV_hat: exceptionV_hat,
			ExceptionX_hat: exceptionX_hat,
			Round:          Round}
		return sn.PutUp(view, &SigningMessage{
			Type: Response,
			View: view,
			Rm:   rm})
	}

	// root reports round is done
	if isroot {
		sn.done <- Round
	}
	return err
}

// Called *only* by root node after receiving all commits
func (sn *Node) FinalizeCommits(view int, Round int) error {
	//Round := sn.Round // *only* in root
	round := sn.Rounds[Round]

	// challenge = Hash(Merkle Tree Root/ Announcement Message, sn.Log.V_hat)
	if sn.Type == PubKey {
		round.c = hashElGamal(sn.suite, sn.LogTest, round.Log.V_hat)
	} else {
		round.c = hashElGamal(sn.suite, round.MTRoot, round.Log.V_hat)
	}

	proof := make([]hashid.HashId, 0)
	err := sn.Challenge(view, &ChallengeMessage{
		C:      round.c,
		MTRoot: round.MTRoot,
		Proof:  proof,
		Round:  Round})
	return err
}

// Called by every node after receiving aggregate responses from descendants
func (sn *Node) VerifyResponses(view, Round int) error {
	round := sn.Rounds[Round]

	// Check that: base**r_hat * X_hat**c == V_hat
	// Equivalent to base**(r+xc) == base**(v) == T in vanillaElGamal
	Aux := sn.suite.Point()
	V_clean := sn.suite.Point()
	V_clean.Add(V_clean.Mul(nil, round.r_hat), Aux.Mul(round.X_hat, round.c))
	// T is the recreated V_hat
	T := sn.suite.Point().Null()
	T.Add(T, V_clean)
	T.Add(T, round.exceptionV_hat)

	var c2 abstract.Secret
	isroot := sn.IsRoot(view)
	if isroot {
		// round challenge must be recomputed given potential
		// exception list
		if sn.Type == PubKey {
			round.c = hashElGamal(sn.suite, sn.LogTest, round.Log.V_hat)
			c2 = hashElGamal(sn.suite, sn.LogTest, T)
		} else {
			round.c = hashElGamal(sn.suite, round.MTRoot, round.Log.V_hat)
			c2 = hashElGamal(sn.suite, round.MTRoot, T)
		}
	}

	// intermediary nodes check partial responses aginst their partial keys
	// the root node is also able to check against the challenge it emitted
	if !T.Equal(round.Log.V_hat) || (isroot && !round.c.Equal(c2)) {
		// if coco.DEBUG == true {
		panic(sn.Name() + "reports ElGamal Collective Signature failed for Round" + strconv.Itoa(Round))
		// }
		// return errors.New("Veryfing ElGamal Collective Signature failed in " + sn.Name() + "for round " + strconv.Itoa(Round))
	}

	if isroot {
		log.Println(sn.Name(), "reports ElGamal Collective Signature succeeded for round", Round)
		// log.Println(round.MTRoot)
	}
	return nil
}

func (sn *Node) PutUpError(view int, err error) {
	// log.Println(sn.Name(), "put up response with err", err)
	sn.PutUp(view, &SigningMessage{
		Type: Error,
		View: view,
		Err:  &ErrorMessage{Err: err.Error()}})
}

// Returns a secret that depends on on a message and a point
func hashElGamal(suite abstract.Suite, message []byte, p abstract.Point) abstract.Secret {
	pb, _ := p.MarshalBinary()
	c := suite.Cipher(pb)
	c.Message(nil, nil, message)
	return suite.Secret().Pick(c)
}

// Called when log for round if full and ready to be hashed
func (sn *Node) HashLog(Round int) error {
	round := sn.Rounds[Round]
	var err error
	round.HashedLog, err = sn.hashLog(Round)
	return err
}

// Auxilary function to perform the actual hashing of the log
func (sn *Node) hashLog(Round int) ([]byte, error) {
	round := sn.Rounds[Round]

	h := sn.suite.Hash()
	logBytes, err := round.Log.MarshalBinary()
	if err != nil {
		return nil, err
	}
	h.Write(logBytes)
	return h.Sum(nil), nil
}
