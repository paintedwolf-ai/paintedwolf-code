"""A fixed pointer operation admits only an unchanged shell-owned delivery and exact scale mapping."""
import copy
import unittest

from linux_release.installed_input import notification_wire as wire


def proof():
    launch = {'pid':123,'parent_pid':1,'start_time_ticks':42}
    engine = {'pid':124,'parent_pid':123,'start_time_ticks':43}
    main = {'bus_name':':1.37','object_path':'/org/a11y/atspi/accessible/1','process':launch,'lineage':[launch]}
    shell = {'bus_name':':1.28','process':{'pid':200,'parent_pid':1,'start_time_ticks':50}}
    service = {'bus_name':':1.45','process':{'pid':201,'parent_pid':1,'start_time_ticks':51}}
    def node(path):return {'bus_name':':1.0','path':'/org/a11y/atspi/accessible/'+path,'pid':200}
    target,app,title,body = node('8'),node('root'),node('12'),node('13')
    return {'surface':{'launch':launch,'engine':engine,'main':main,'control':None,'surfaces':[
        {'identity':main,'kind':'frame','visible':False,'active':False,'defunct':False,'modal':False,'parent':None}]},
        'service':service,'shell':shell,'delivery':{'notification_id':1,'request_serial':32,
            'sender':':1.60','title_sha256':'a'*64,'body_sha256':'b'*64},
        'geometry':{'display_owner':shell,'serial':9,'logical_extent':[0,0,1024,768],
            'pixel_size':[1024,768],'scale_percent':100,'transform':0,'layout_mode':2,'shell_extent':[0,0,1024,768]},
        'target':{'identity':target,'ancestry':[target,app],'role':101,'extent':[295,36,500,96],
            'visible':True,'showing':True,'enabled':True,'sensitive':True,'labels':[
                {'identity':title,'ancestry':[title,target],'role':29,'name_sha256':'a'*64},
                {'identity':body,'ancestry':[body,target],'role':29,'name_sha256':'b'*64}]}}


class NotificationWireTests(unittest.TestCase):
    def test_host_derives_center_and_sealed_mouse_press_release(self):
        value = proof()
        events = wire.qmp_events(value)
        self.assertEqual(events[:2],[{'type':'abs','data':{'axis':'x','value':17440}},
            {'type':'abs','data':{'axis':'y','value':3567}}])
        self.assertEqual(events[2:],[{'type':'btn','data':{'down':True,'button':'left'}},
            {'type':'btn','data':{'down':False,'button':'left'}}])
        self.assertEqual(len(wire.event_sha256(value)),64)
        changed = copy.deepcopy(value);changed['target']['extent'][0] += 1
        self.assertNotEqual(wire.event_sha256(value),wire.event_sha256(changed))
        with self.assertRaises(ValueError):wire.before({**value,'coordinates':[544,84]})

    def test_mapping_refuses_scale_rotation_multiple_or_offset_display_geometry(self):
        for key,replacement in [('scale_percent',125),('transform',1),('layout_mode',1),
                ('pixel_size',[2048,1536]),('logical_extent',[1024,0,1024,768]),
                ('shell_extent',[0,0,1023,768])]:
            value = proof();value['geometry'][key]=replacement
            with self.subTest(key=key),self.assertRaises(ValueError):wire.before(value)

    def test_target_must_be_visible_actionable_notification_inside_display(self):
        for key,replacement in [('role',39),('showing',False),('visible',False),('sensitive',False),
                ('enabled',False),('extent',[800,0,500,96]),('extent',[-1,36,500,96])]:
            value = proof();value['target'][key]=replacement
            with self.subTest(key=key),self.assertRaises(ValueError):wire.before(value)

    def test_delivered_labels_require_distinct_real_descendants_and_exact_hashes(self):
        for kind in ('hash','parent','process','duplicate'):
            value = proof();labels=value['target']['labels']
            if kind=='hash':labels[0]['name_sha256']='c'*64
            elif kind=='parent':labels[0]['ancestry'][-1]=labels[1]['identity']
            elif kind=='process':labels[0]['identity']['pid']=201
            else:labels[1]=labels[0]
            with self.subTest(kind=kind),self.assertRaises(ValueError):wire.before(value)

    def test_changed_owner_extent_ancestry_and_delivery_cannot_reuse_predispatch_proof(self):
        initial = proof()
        for kind in ('owner','extent','ancestry','delivery','mode'):
            current=copy.deepcopy(initial)
            if kind=='owner':current['service']['bus_name']=':1.99'
            elif kind=='extent':current['target']['extent'][0]+=1
            elif kind=='ancestry':current['target']['ancestry'][-1]['path']+='_replacement'
            elif kind=='delivery':current['delivery']['notification_id']=2
            else:current['geometry']['serial']+=1
            with self.subTest(kind=kind),self.assertRaises(ValueError):wire.same_target(initial,current)

    def test_notification_and_shell_service_processes_may_be_distinct(self):
        value=proof()
        self.assertNotEqual(value['service']['process'],value['shell']['process'])
        self.assertIs(wire.before(value),value)
        value['service']['process']=value['surface']['main']['process']
        with self.assertRaises(ValueError):wire.before(value)


if __name__=='__main__':unittest.main()
